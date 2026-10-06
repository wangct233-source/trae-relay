// auth-helper：trae-relay 本机授权回调监听器（Go 版，对应 trae2api-cn 的 web_login.py）。
//
// Trae 网页授权强制回调到 http://127.0.0.1:<port>/authorize，浏览器沙箱
// 无法监听本机端口，因此需要一个本机轻量监听器：接收授权回调 → 解析凭证
// → 转发 relay /api/web-auth 入库 → postMessage 通知授权页结果。
//
// 用法：auth-helper.exe --relay http://server:8080 [--port 8765]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	relay    = flag.String("relay", "http://127.0.0.1:8080", "relay 服务器地址")
	port     = flag.Int("port", 8765, "本机监听端口")
	clientID = flag.String("client-id", "ono9krqynydwx5", "Trae Client ID")
	authURL  = flag.String("auth-url", "https://www.trae.cn/authorization", "Trae 授权页")
)

func main() {
	flag.Parse()
	relayHost := strings.TrimRight(*relay, "/")
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("auth-helper 启动：本机 http://%s  → relay %s", addr, relayHost)
	log.Printf("请在浏览器打开控制台授权页（或本页）完成 Trae 授权")
	mux := http.NewServeMux()
	mux.HandleFunc("/", pageIndex)
	mux.HandleFunc("/authorize", handleAuthorize(relayHost))
	mux.HandleFunc("/healthz", handleHealthz(relayHost))
	mux.HandleFunc("/relay-url", handleRelayURL(relayHost))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// ---------- 页面 ----------

func pageHTML(content string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>trae-relay 授权</title>
<style>*{margin:0;padding:0;box-sizing:border-box}
body{font:15px system-ui,"Segoe UI","Microsoft YaHei",sans-serif;background:#0f1117;color:#e8eaed;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px}
.panel{background:#1a1d28;border-radius:8px;max-width:460px;width:100%;padding:28px 24px;border:1px solid #2d3140}
h1{font-size:18px;font-weight:600;margin-bottom:12px}p{color:#9aa0b0;font-size:13px;line-height:1.6;margin-bottom:12px}
.btn{display:inline-flex;align-items:center;padding:10px 20px;border-radius:6px;font-size:14px;cursor:pointer;border:1px solid transparent;text-decoration:none;color:#fff}
.btn-primary{background:#1a8c5c}.btn-primary:hover{background:#14a06a}.btn-primary:disabled{opacity:.5;cursor:not-allowed}
.btn-ghost{background:transparent;border-color:#3a3f54;color:#9aa0b0}
.btn-group{display:flex;gap:8px;margin-top:12px}
.msg{margin-top:12px;padding:8px 12px;border-radius:6px;font-size:13px;display:none}
.msg-ok{background:#1f6c3a;color:#a8e6b8;display:block}.msg-err{background:#6c1f1f;color:#e6a8a8;display:block}
.loading{margin-top:12px;display:none;font-size:13px;color:#9aa0b0}
code{background:#252836;padding:1px 6px;border-radius:4px;font-size:12px}</style></head>
<body><div class="panel">` + content + `</div></body></html>`
}

func pageIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := pageHTML(fmt.Sprintf(`
<h1>trae-relay 网页授权</h1>
<p>监听端口: <code>%d</code><br>中转站: <code>%s</code></p>
<p>1. 确保浏览器已登录 <a href="https://www.trae.cn" target="_blank" style="color:#8ab4f8">trae.cn</a><br>2. 点击下方按钮完成授权</p>
<div class="btn-group">
  <button class="btn btn-primary" id="auth-btn" onclick="startAuth()">使用 Trae 网页授权登录</button>
  <a class="btn btn-ghost" href="https://www.trae.cn" target="_blank">打开 trae.cn</a>
</div>
<div id="loading" class="loading">等待授权中…</div>
<div id="auth-msg" class="msg"></div>
<script>
var state={traceId:null,win:null};
function uuid(){return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,function(c){var r=Math.random()*16|0;return(c==='x'?r:(r&3|8)).toString(16)})}
function randomHex(n){var a=new Uint8Array(n);crypto.getRandomValues(a);return Array.from(a,b=>b.toString(16).padStart(2,'0')).join('')}
function randomDigits(n){var s='';while(s.length<n)s+=Math.floor(Math.random()*1e10).toString();return s.slice(0,n)}
function buildAuthUrl(){
  var cb='http://127.0.0.1:%d/authorize';
  var mid=randomHex(32),did=randomDigits(19),tid=state.traceId;
  var p=new URLSearchParams({login_version:'1',auth_from:'solo',login_channel:'native_ide',plugin_version:'2.3.24254',
    auth_type:'local',client_id:'%s',redirect:'0',login_trace_id:tid,
    auth_callback_url:cb,machine_id:mid,device_id:did,x_device_id:did,x_machine_id:mid,
    x_device_brand:'Mac14,7',x_device_type:'mac',x_os_version:'macOS 26.4.1',x_env:'',
    x_app_version:'0.1.7',x_app_type:'stable',hide_saas_login:'true'});
  return '%s?'+p.toString();
}
function startAuth(){
  state.traceId=uuid();
  var w=window.open(buildAuthUrl(),'trae-relay-oauth','width=560,height=760');
  if(!w){showMsg('弹出窗口被拦截',false);return}
  state.win=w;
  document.getElementById('loading').style.display='block';
  document.getElementById('auth-btn').disabled=true;
  var poll=setInterval(function(){if(w.closed){clearInterval(poll);
    document.getElementById('loading').style.display='none';
    document.getElementById('auth-btn').disabled=false;}},700);
}
window.addEventListener('message',function(ev){
  if(!ev.data||ev.data.type!=='trae-relay-web-login')return;
  if(state.traceId&&ev.data.loginTraceId!==state.traceId)return;
  showMsg(ev.data.success?'授权成功，凭证已写入服务器':(ev.data.error||'授权失败'),ev.data.success);
  document.getElementById('loading').style.display='none';
  document.getElementById('auth-btn').disabled=false;
  if(state.win&&!state.win.closed)state.win.close();
});
function showMsg(t,ok){var el=document.getElementById('auth-msg');el.textContent=t;el.className='msg'+(ok?' msg-ok':' msg-err')}
</script>`, *port, *relay, *port, *clientID, *authURL))
	writeHTML(w, page)
}

// ---------- 回调处理 ----------

func handleAuthorize(relayHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		traceID := q.Get("loginTraceID")
		if traceID == "" {
			traceID = q.Get("login_trace_id")
		}
		creds := parseOAuthParams(q)
		success, msg := false, "未收到有效的 userJwt/refreshToken，请确认已登录 trae.cn"
		if creds["token"] != "" || creds["refreshToken"] != "" {
			ok, m := forwardToRelay(relayHost, creds)
			success, msg = ok, m
		}
		writeHTML(w, oauthResultPage(success, msg, traceID))
	}
}

// parseOAuthParams 解析 Trae 授权回调参数（对照 trae2api-cn parse_oauth_params）。
// 新流程回调带 userJwt JSON；老流程只带 refreshToken（由 relay 端换 JWT）。
func parseOAuthParams(q url.Values) map[string]string {
	jload := func(key string) map[string]any {
		var m map[string]any
		if v := q.Get(key); v != "" {
			_ = json.Unmarshal([]byte(v), &m)
		}
		return m
	}
	get := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	userJwt := jload("userJwt")
	userInfo := jload("userInfo")
	token := get(userJwt, "Token", "token")
	refresh := get(userJwt, "RefreshToken", "refreshToken")
	if refresh == "" {
		refresh = firstNonEmpty(q.Get("refreshToken"), q.Get("data"))
	}
	userID := get(userInfo, "UserID", "userId", "userID")
	if userID == "" {
		userID = q.Get("userId")
	}
	region := get(userInfo, "Region", "region")
	if region == "" {
		region = firstNonEmpty(q.Get("region"), "CN")
	}
	clientID := get(userJwt, "ClientID", "clientId")
	if clientID == "" {
		clientID = firstNonEmpty(q.Get("clientID"), q.Get("clientId"), q.Get("client_id"))
	}
	host := firstNonEmpty(q.Get("host"), get(userInfo, "Host", "host"))
	return map[string]string{
		"token":          token,
		"refreshToken":   refresh,
		"userId":         userID,
		"tenantId":       get(userInfo, "TenantID", "tenantId"),
		"region":         region,
		"host":           host,
		"clientId":       clientID,
		"webId":          get(userInfo, "WebId", "webId"),
		"bizUserId":      get(userInfo, "BizUserId", "bizUserId"),
		"userUniqueId":   get(userInfo, "UserUniqueId", "userUniqueId"),
		"scope":          firstNonEmpty(q.Get("scope"), get(userInfo, "Scope", "scope")),
		"tenant":         get(userInfo, "Tenant", "tenant"),
		"userRegion":     firstNonEmpty(q.Get("userRegion"), get(userInfo, "UserRegion", "userRegion")),
		"userIdentity":   get(userInfo, "UserIdentity", "userIdentity"),
		"screenName":     get(userInfo, "ScreenName", "screenName"),
		"expiredAt":      get(userJwt, "TokenExpireAt", "tokenExpireAt"),
		"refreshExpireAt": get(userJwt, "RefreshExpireAt", "refreshExpireAt"),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// forwardToRelay 把凭证 POST 到 relay /api/web-auth。
func forwardToRelay(relayHost string, creds map[string]string) (bool, string) {
	raw, _ := json.Marshal(creds)
	resp, err := http.Post(relayHost+"/api/web-auth", "application/json", bytes.NewReader(raw))
	if err != nil {
		return false, "转发失败: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &out) == nil {
		if out.Success {
			return true, "凭证已写入服务器账号池"
		}
		return false, out.Error
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Sprintf("relay 返回 %d", resp.StatusCode)
	}
	return true, "凭证已提交"
}

func oauthResultPage(success bool, msg, traceID string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace
	delay := 4000
	if success {
		delay = 1200
	}
	return pageHTML(fmt.Sprintf(`
<div class="msg %s" style="display:block"><h2 style="margin:0 0 8px;font-size:16px">%s</h2><p>%s</p></div>
<script>(function(){try{
  if(window.opener){window.opener.postMessage({type:'trae-relay-web-login',success:%s,error:%s,loginTraceId:'%s'},'*')}
}catch(e){};setTimeout(function(){window.close()},%d)})();</script>`,
		map[bool]string{true: "msg-ok", false: "msg-err"}[success],
		map[bool]string{true: "成功", false: "失败"}[success],
		esc(msg), map[bool]string{true: "true", false: "false"}[success],
		esc(msg), esc(traceID), delay))
}

// ---------- 辅助端点 ----------

func handleHealthz(relayHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "relay": relayHost, "port": *port})
	}
}

func handleRelayURL(relayHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"relay": relayHost})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func writeHTML(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}
