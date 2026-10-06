package admin

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"

	"trae-relay/internal/account"
	"trae-relay/internal/checkin"
	"trae-relay/internal/relay"
	"trae-relay/internal/updater"
	"trae-relay/internal/usage"
)

// Handler 管理 API 装配体。
type Handler struct {
	Pool     *account.Pool
	Runtime  *Runtime
	Checkin  *checkin.Scheduler
	Service  *relay.Service
	Usage    *usage.Tracker
	Upstream *account.UpstreamClient
	Updater  *updater.Updater
	EnvAPIKeys []string // env API_KEYS（与运行时设置合并生效与展示）
}

// Register 注册全部管理路由（mux 上挂在 /api/ 与 /admin）。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/overview", h.handleOverview)
	mux.HandleFunc("GET /api/endpoint-info", h.handleEndpointInfo)
	mux.HandleFunc("POST /api/endpoint/generate-key", h.handleGenerateKey)
	mux.HandleFunc("POST /api/endpoint/delete-key", h.handleDeleteKey)
	mux.HandleFunc("GET /api/records", h.handleRecords)
	mux.HandleFunc("GET /api/accounts", h.handleAccounts)
	mux.HandleFunc("POST /api/accounts/import", h.handleImport)
	mux.HandleFunc("POST /api/accounts/login", h.handlePasswordLogin)
	mux.HandleFunc("POST /api/accounts/{id}/toggle", h.handleToggle)
	mux.HandleFunc("DELETE /api/accounts/{id}", h.handleRemove)
	mux.HandleFunc("POST /api/accounts/{id}/checkin", h.handleCheckinOne)
	mux.HandleFunc("POST /api/accounts/{id}/credits", h.handleCreditsOne)
	mux.HandleFunc("POST /api/checkin/all", h.handleCheckinAll)
	mux.HandleFunc("GET /api/settings", h.handleGetSettings)
	mux.HandleFunc("POST /api/settings", h.handleSetSettings)
	mux.HandleFunc("GET /api/update/check", h.handleUpdateCheck)
	mux.HandleFunc("POST /api/update/apply", h.handleUpdateApply)
	mux.HandleFunc("GET /api/update/status", h.handleUpdateStatus)
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	days := atoiDefault(r.URL.Query().Get("days"), 7)
	daily, total := h.Usage.Overview(days)
	totalC, enabledC := h.Pool.Count()
	var credits *float64
	unlimited := false
	for _, a := range h.Pool.List() {
		if !a.Enabled {
			continue
		}
		if a.CreditsUnlimited {
			unlimited = true
		} else if a.CreditsRemaining != nil {
			if credits == nil {
				v := *a.CreditsRemaining
				credits = &v
			} else {
				*credits += *a.CreditsRemaining
			}
		}
	}
	type row = struct {
		Day  string
		Data usage.DayBucket
	}
	JSON(w, 200, map[string]any{
		"accounts_total": totalC, "accounts_enabled": enabledC,
		"credits": credits, "credits_unlimited": unlimited,
		"daily": daily, "total": total,
		"checkin": h.Checkin.Status(),
	})
}

// handleEndpointInfo GET /api/endpoint-info —— 对话 API 接入信息（地址 + Key 列表）。
func (h *Handler) handleEndpointInfo(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	base := fmt.Sprintf("%s://%s/v1", scheme, r.Host)
	merged := mergeKeys(h.EnvAPIKeys, h.Runtime.Get().APIKeys)
	masked := make([]map[string]any, 0, len(merged))
	for _, k := range merged {
		if len(k) > 10 {
			masked = append(masked, map[string]any{"key": k, "preview": k[:6] + "***" + k[len(k)-4:]})
		} else {
			masked = append(masked, map[string]any{"key": k, "preview": k})
		}
	}
	JSON(w, 200, map[string]any{
		"base_url": base, "api_keys": masked, "auth_required": len(merged) > 0,
		"curl_example": fmt.Sprintf(`curl %s/chat/completions -H "Authorization: Bearer <KEY>" -H "Content-Type: application/json" -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'`, base),
	})
}

// handleGenerateKey POST /api/endpoint/generate-key —— 生成随机 Key 并存入运行时设置。
func (h *Handler) handleGenerateKey(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	key := "sk-" + hexEncode(raw)
	st := h.Runtime.Get()
	st.APIKeys = mergeKeys(st.APIKeys, []string{key})
	if err := h.Runtime.Set(st); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"success": true, "key": key, "api_keys": st.APIKeys})
}

// handleDeleteKey POST /api/endpoint/delete-key {key}
func (h *Handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	m, err := body(r)
	if err != nil {
		JSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	key := str(m, "key")
	st := h.Runtime.Get()
	out := st.APIKeys[:0]
	for _, k := range st.APIKeys {
		if k != key {
			out = append(out, k)
		}
	}
	st.APIKeys = out
	if err := h.Runtime.Set(st); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"success": true, "api_keys": st.APIKeys})
}

func mergeKeys(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, l := range lists {
		for _, k := range l {
			if k != "" && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&15]
	}
	return string(out)
}

func (h *Handler) handleRecords(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	JSON(w, 200, map[string]any{"records": h.Usage.Records(limit)})
}

func (h *Handler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list := h.Pool.List()
	// 脱敏 token
	for i := range list {
		if t := list[i].Token; len(t) > 12 {
			list[i].Token = t[:8] + "***" + t[len(t)-4:]
		}
	}
	JSON(w, 200, map[string]any{"accounts": list})
}

func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	m, err := body(r)
	if err != nil {
		JSON(w, 400, map[string]any{"error": "请求体解析失败: " + err.Error()})
		return
	}
	text := str(m, "json", "text", "content")
	added, updated, err := h.Pool.ImportJSON(text)
	if err != nil {
		JSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"added": added, "updated": updated})
}

func (h *Handler) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	m, err := body(r)
	if err != nil {
		JSON(w, 400, map[string]any{"error": "请求体解析失败: " + err.Error()})
		return
	}
	phone := str(m, "phone", "account", "username")
	password := str(m, "password")
	if phone == "" || password == "" {
		JSON(w, 400, map[string]any{"error": "账号与密码必填"})
		return
	}
	acc, err := h.Pool.ImportPassword(r.Context(), phone, password, str(m, "captcha"))
	if err != nil {
		JSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"id": acc.ID, "user_id": acc.UserID, "label": acc.Label})
}

func (h *Handler) handleToggle(w http.ResponseWriter, r *http.Request) {
	m, err := body(r)
	if err != nil {
		JSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	enabled, ok := boolean(m, "enabled")
	if !ok {
		JSON(w, 400, map[string]any{"error": "enabled 字段必填"})
		return
	}
	id := r.PathValue("id")
	if err := h.Pool.SetEnabled(id, enabled); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"ok": true})
}

func (h *Handler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if err := h.Pool.Remove(r.PathValue("id")); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, map[string]any{"ok": true})
}

func (h *Handler) handleCheckinOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, a := range h.Pool.List() {
		if a.ID == id {
			res := h.Checkin.CheckinOne(r.Context(), a)
			JSON(w, 200, res)
			return
		}
	}
	JSON(w, 404, map[string]any{"error": "账号不存在"})
}

func (h *Handler) handleCreditsOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, a := range h.Pool.List() {
		if a.ID == id {
			ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
			defer cancel()
			remaining, total, unlimited, ok, err := h.Upstream.FetchCredits(ctx, a.Token, deviceIDOf(a))
			if err != nil {
				JSON(w, 502, map[string]any{"error": err.Error()})
				return
			}
			if ok {
				h.Pool.UpdateCredits(id, remaining, total, unlimited)
			}
			JSON(w, 200, map[string]any{
				"remaining": remaining, "total": total, "unlimited": unlimited, "known": ok,
			})
			return
		}
	}
	JSON(w, 404, map[string]any{"error": "账号不存在"})
}

func (h *Handler) handleCheckinAll(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), allCheckinTimeout)
		defer cancel()
		_ = h.Checkin.CheckinAll(ctx)
	}()
	JSON(w, 200, map[string]any{"ok": true, "message": "签到任务已在后台启动"})
}

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	JSON(w, 200, h.Runtime.Get())
}

func (h *Handler) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	m, err := body(r)
	if err != nil {
		JSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	cur := h.Runtime.Get()
	if s := str(m, "scheduling"); s != "" {
		if s != "round-robin" && s != "credit" {
			JSON(w, 400, map[string]any{"error": "scheduling 仅支持 round-robin / credit"})
			return
		}
		cur.Scheduling = s
	}
	if v, ok := boolean(m, "auto_checkin"); ok {
		cur.AutoCheckin = v
	}
	if s := str(m, "checkin_time"); s != "" {
		if len(s) != 5 || s[2] != ':' {
			JSON(w, 400, map[string]any{"error": "checkin_time 格式应为 HH:MM"})
			return
		}
		cur.CheckinTime = s
	}
	if v, ok := boolean(m, "auto_update"); ok {
		cur.AutoUpdate = v
	}
	if err := h.Runtime.Set(cur); err != nil {
		JSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, cur)
}

func (h *Handler) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), updateTimeout)
	defer cancel()
	rel, err := h.Updater.Check(ctx)
	if err != nil {
		JSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	JSON(w, 200, rel)
}

func (h *Handler) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	version := ""
	if m, err := body(r); err == nil {
		version = str(m, "version")
	}
	// 后台执行下载与替换；替换成功后进程会以新二进制重启
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateApplyTimeout)
		defer cancel()
		if _, err := h.Updater.Apply(ctx, strings.TrimSpace(version)); err != nil {
			_ = err // 控制台通过 /api/update/status 查看结果
		}
	}()
	JSON(w, 200, map[string]any{"ok": true, "message": "更新任务已启动，完成后服务将自动重启"})
}

func (h *Handler) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	JSON(w, 200, h.Updater.Status())
}
