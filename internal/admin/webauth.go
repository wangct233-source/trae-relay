package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"trae-relay/internal/account"
)

// WebAuthDeps 网页授权依赖。
type WebAuthDeps struct {
	Pool     *account.Pool
	Upstream *account.UpstreamClient
}

// webAuthCreds 凭证入参：本机助手转发 JSON，或手动粘贴回调链接（callback_url）。
type webAuthCreds struct {
	CallbackURL  string `json:"callback_url"`
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	UserID       string `json:"userId"`
	ScreenName   string `json:"screenName"`
	Region       string `json:"region"`
	ClientID     string `json:"clientId"`
	Host         string `json:"host"`
}

// jsonDecodeBody 解析 JSON 请求体。
func jsonDecodeBody(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(v)
}

// HandleWebAuth POST /api/web-auth —— 授权凭证入池。
// 两种入参：① 本机助手转发的凭证 JSON；② 手动粘贴的完整回调链接 {callback_url}。
// 免管理密码：能出示有效回调凭证本身即代表完成了 Trae 登录授权。
func (d *WebAuthDeps) HandleWebAuth(w http.ResponseWriter, r *http.Request) {
	var c webAuthCreds
	if err := jsonDecodeBody(r, &c); err != nil {
		JSON(w, 400, map[string]any{"success": false, "error": "请求体解析失败: " + err.Error()})
		return
	}
	// 手动粘贴模式：解析回调链接里的 query
	if c.Token == "" && c.RefreshToken == "" && c.CallbackURL != "" {
		parsed, err := account.ParseCallbackURL(c.CallbackURL)
		if err != nil {
			JSON(w, 400, map[string]any{"success": false, "error": "回调链接解析失败: " + err.Error()})
			return
		}
		c.Token = parsed["token"]
		c.RefreshToken = parsed["refreshToken"]
		c.UserID = orDefault(c.UserID, parsed["userId"])
		c.ScreenName = orDefault(c.ScreenName, parsed["screenName"])
		c.Region = orDefault(c.Region, parsed["region"])
		c.ClientID = orDefault(c.ClientID, parsed["clientId"])
		c.Host = orDefault(c.Host, parsed["host"])
	}
	if c.Token == "" && c.RefreshToken == "" {
		JSON(w, 400, map[string]any{"success": false, "error": "缺少 token/refreshToken/callback_url"})
		return
	}
	// 老流程：只有 refreshToken，向 Trae 兑换 Cloud-IDE-JWT
	if c.Token == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		token, expiry, userID, err := d.Upstream.ExchangeToken(ctx, c.RefreshToken, orDefault(c.ClientID, "ono9krqynydwx5"))
		if err != nil {
			JSON(w, 400, map[string]any{"success": false, "error": "ExchangeToken 失败: " + err.Error()})
			return
		}
		c.Token = token
		if c.UserID == "" {
			c.UserID = userID
		}
		_ = expiry
	}
	id := c.UserID
	if id == "" {
		id = account.JWTUser(c.Token)
	}
	if id == "" {
		id = fmt.Sprintf("web-%d", time.Now().UnixNano())
	}
	acc := &account.Account{
		ID: id, UserID: c.UserID, Token: c.Token, RefreshToken: c.RefreshToken,
		Label: orDefault(c.ScreenName, "网页授权"), Region: orDefault(c.Region, "CN"),
		Enabled: true, CreatedAt: time.Now().Unix(),
	}
	acc.TokenExpiry = account.JWTExpiry(c.Token)
	if err := d.Pool.UpsertWebAuth(acc); err != nil {
		JSON(w, 500, map[string]any{"success": false, "error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"success": true, "id": acc.ID, "label": acc.Label})
}

// HandleLoginPage GET /web/login —— 授权引导页（检测本机助手 / 下载启动器）。
func (d *WebAuthDeps) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	page := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>trae-relay 网页授权</title>
<style>*{margin:0;padding:0;box-sizing:border-box}
body{font:15px system-ui,"Segoe UI","Microsoft YaHei",sans-serif;background:#f6f7f9;color:#1c2430;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px}
.panel{background:#fff;border:1px solid #e3e7ee;border-radius:12px;max-width:480px;width:100%;padding:28px 26px}
h1{font-size:18px;margin-bottom:10px}p{color:#6b7686;font-size:13px;line-height:1.7;margin-bottom:10px}
.btn{display:inline-flex;align-items:center;padding:10px 18px;border-radius:8px;font-size:14px;cursor:pointer;border:1px solid transparent;text-decoration:none;color:#fff;background:#2563eb;margin-right:8px}
.btn.ghost{background:#fff;border-color:#e3e7ee;color:#1c2430}
.btn:disabled{opacity:.5;cursor:not-allowed}
#state{margin:12px 0;padding:10px 12px;border-radius:8px;font-size:13px;background:#f1f5f9}
code{background:#eef2f7;padding:1px 6px;border-radius:4px;font-size:12px}
ol{margin:0 0 10px 18px;color:#6b7686;font-size:13px;line-height:1.8}</style></head>
<body><div class="panel">
<h1>trae-relay 网页授权登录</h1>
<p>Trae 授权页强制回调到 <code>http://127.0.0.1:8765/authorize</code>，浏览器无法监听本机端口，
因此需要一个<b>本机授权助手</b>接收回调并转发给服务器（这是 Trae 的限制，不是本项目缺陷）。</p>
<div id="state">正在检测本机授权助手…</div>
<div>
  <button class="btn" id="auth-btn" onclick="startAuth()">使用 Trae 网页授权登录</button>
  <a class="btn ghost" href="/web/login/download">下载一键启动 start_auth.bat</a>
</div>
<ol style="margin-top:14px">
  <li>下载并运行 <code>start_auth.bat</code>（保持窗口开启）</li>
  <li>回到本页，检测通过后点击「网页授权登录」</li>
  <li>在弹出的 Trae 页面确认登录，凭证自动写入账号池</li>
</ol>
<p style="font-size:12px">已在本机运行过助手？直接刷新本页即可。</p>
</div>
<script>
var HELPER='http://127.0.0.1:8765';
function setState(html,ok){var el=document.getElementById('state');el.innerHTML=html;
  el.style.background=ok?'#dcfce7':'#f1f5f9';el.style.color=ok?'#166534':'#1c2430'}
async function detect(){
  try{
    const ctrl=new AbortController();setTimeout(()=>ctrl.abort(),2500);
    const r=await fetch(HELPER+'/healthz',{signal:ctrl.signal});
    if(r.ok){setState('✅ 本机授权助手在线，点击上方按钮开始授权',true);
      document.getElementById('auth-btn').disabled=false;return true}
  }catch(e){}
  setState('❌ 未检测到本机授权助手：请先下载 <code>start_auth.bat</code> 双击运行（Windows，保持窗口开启），然后刷新本页');
  document.getElementById('auth-btn').disabled=true;return false;
}
function startAuth(){window.open(HELPER+'/','trae-relay-oauth','width=560,height=760')}
detect();
</script></body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

// HandleLoginDownload GET /web/login/download —— 动态生成 start_auth.bat（内嵌 relay 地址）。
func (d *WebAuthDeps) HandleLoginDownload(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	relay := fmt.Sprintf("%s://%s", scheme, r.Host)
	bat := "@echo off\r\n" +
		"rem trae-relay 一键网页授权（Windows）\r\n" +
		"rem 运行后回到浏览器授权页点击授权，凭据自动写入服务器。\r\n" +
		"chcp 65001 >nul\r\n" +
		"set RELAY=" + relay + "\r\n" +
		"set PORT=8765\r\n" +
		"set EXE=%TEMP%\\trae-auth-helper.exe\r\n" +
		"echo ================================================\r\n" +
		"echo  trae-relay 本机授权助手\r\n" +
		"echo  中转站: %RELAY%\r\n" +
		"echo  本机回调: http://127.0.0.1:%PORT%/authorize\r\n" +
		"echo ================================================\r\n" +
		"echo 正在下载授权助手（首次约 5MB）...\r\n" +
		"powershell -NoProfile -Command \"[Net.ServicePointManager]::SecurityProtocol='Tls12'; Invoke-WebRequest -Uri 'https://github.com/wangct233-source/trae-relay/releases/latest/download/auth-helper-windows-amd64.exe' -OutFile '%EXE%' -UseBasicParsing\"\r\n" +
		"if not exist \"%EXE%\" (\r\n" +
		"  echo 下载失败，请检查网络后重试。\r\n" +
		"  pause\r\n" +
		"  exit /b 1\r\n" +
		")\r\n" +
		"\"%EXE%\" --relay %RELAY% --port %PORT%\r\n" +
		"pause\r\n"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="start_auth.bat"`)
	_, _ = w.Write([]byte(bat))
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
