// Package relay 实现 Trae remote chat_sessions 上游协议与 OpenAI 兼容出口。
package relay

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Upstream remote chat_sessions 上游客户端。
type Upstream struct {
	BaseURL string
	HTTP    *http.Client
}

func NewUpstream(baseURL string) *Upstream {
	return &Upstream{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{},
	}
}

// Message OpenAI Chat 消息（仅本转发层用到的字段）。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name,omitempty"`
}

// Text 把 content（string 或分段数组）拼接为纯文本。
func (m Message) Text() string {
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// FlattenQuery 把多轮消息扁平化为单条 query（对照上游网页端行为）。
func FlattenQuery(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		text := strings.TrimSpace(m.Text())
		if text == "" {
			continue
		}
		switch m.Role {
		case "system":
			b.WriteString("[System Instructions]\n" + text + "\n\n")
		case "assistant":
			b.WriteString("[Previous Assistant Response]\n" + text + "\n\n")
		case "tool":
			b.WriteString("[Tool Result]\n" + text + "\n\n")
		default:
			b.WriteString("[User]\n" + text + "\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// SessionEvent 上游 SSE 事件（名称已归一化）。
type SessionEvent struct {
	Name string
	Data map[string]any
}

func headers(token string, stream bool) map[string]string {
	h := map[string]string{
		"Authorization":          "Cloud-IDE-JWT " + token,
		"Content-Type":           "application/json",
		"X-Trae-Client-Type":     "web",
		"X-Preferenced-Language": "zh-CN",
		"x-user-region":          "CN",
		"Origin":                 "https://solo.trae.cn",
		"Referer":                "https://solo.trae.cn/",
		"User-Agent":             "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36",
		"Accept":                 "text/event-stream",
	}
	if !stream {
		h["Accept"] = "application/json"
	}
	return h
}

// StableSessionID 每账号+模型一个稳定会话键（对照上游 biz_session_id 行为）。
func StableSessionID(accID, model string) string {
	sum := sha256.Sum256([]byte(accID + "\x1f" + model))
	b := sum[:]
	last := binary.BigEndian.Uint64(b[8:16]) & 0xffffffffffff
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8])&0x0fff|0x4000,
		binary.BigEndian.Uint16(b[8:10])&0x3fff|0x8000,
		last,
	)
}

// CreateSession 创建 remote 回合，返回 (sessionID, messageID)。
func (u *Upstream) CreateSession(ctx context.Context, token, model, query, sessionID string) (string, string, error) {
	mode, strategy := "code", "auto"
	modelName := ""
	if model != "" && !strings.EqualFold(model, "auto") {
		modelName = model
		if s := envString("TRAE_MODEL_STRATEGY", "auto"); s == "manual" {
			strategy = "manual"
		}
	}
	agentType := "solo_agent_remote"
	if strings.HasSuffix(strings.ToLower(model), "-work") {
		agentType = "solo_work_remote"
		mode = "work"
	}
	common := map[string]any{
		"language": "zh-cn", "app_language": "zh-CN", "quality": "stable",
		"app_version": "1.0.0.1229", "user_identity": "Free", "is_freshman": "0",
		"scope": "marscode-cn", "tenant": "marscode", "region": "cn", "aiRegion": "CN",
		"is_privacy_mode": 0, "privacy_mode": "off", "solo_chat_mode": mode,
		"biz_session_id": sessionID,
	}
	if v := envString("TRAE_BIZ_USER_ID", ""); v != "" {
		common["biz_user_id"] = v
		common["user_id"] = v
	}
	initial := map[string]any{
		"chat_session_id":          "",
		"content":                  []any{},
		"query":                    query,
		"model_name":               modelName,
		"agent_type":               agentType,
		"agent_id":                 agentType,
		"model_selection_strategy": strategy,
		"common_params":            marshalString(common),
	}
	if raw := envString("TRAE_CUSTOM_MODEL_JSON", ""); raw != "" && strategy == "manual" {
		var cm map[string]any
		if json.Unmarshal([]byte(raw), &cm) == nil {
			initial["custom_model"] = cm
			initial["model_config_source"] = 1
			initial["model_is_preset"] = true
		}
	}
	body := map[string]any{
		"mode": mode, "environment_id": "default", "initial_message": initial,
		"env": "remote", "auto_create_project": false, "origin": "web",
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", u.BaseURL+"/chat_sessions", bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	for k, v := range headers(token, false) {
		req.Header.Set(k, v)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("create_session [%d]: %.300s", resp.StatusCode, data)
	}
	var payload struct {
		Code any `json:"code"`
		Data struct {
			ChatSessionID string `json:"chat_session_id"`
			MessageID     string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", fmt.Errorf("create_session 响应解析失败: %.200s", data)
	}
	if sid, mid := payload.Data.ChatSessionID, payload.Data.MessageID; sid != "" && mid != "" {
		return sid, mid, nil
	}
	return "", "", fmt.Errorf("create_session 缺少会话 ID: %.200s", data)
}

// StreamEvents 读取回合 SSE 事件流直至 done/连接结束。
func (u *Upstream) StreamEvents(ctx context.Context, token, sessionID, messageID string, fn func(SessionEvent) error) error {
	url := fmt.Sprintf("%s/chat_sessions/%s/events?reply_to_message_id=%s", u.BaseURL, sessionID, messageID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers(token, true) {
		req.Header.Set(k, v)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("events [%d]: %.300s", resp.StatusCode, raw)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	eventName := ""
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case strings.HasPrefix(line, ":"):
			// 心跳注释
		case strings.HasPrefix(line, "event:"):
			eventName = normalizeEvent(strings.TrimSpace(line[6:]))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(line[5:])
			if payload == "[DONE]" {
				return nil
			}
			data := map[string]any{}
			if json.Unmarshal([]byte(payload), &data) != nil {
				data = map[string]any{"_raw": payload}
			}
			name := eventName
			if name == "" {
				if v, ok := data["event"].(string); ok {
					name = normalizeEvent(v)
				} else {
					name = "message"
				}
			}
			eventName = ""
			if err := fn(SessionEvent{Name: name, Data: data}); err != nil {
				return err
			}
			if name == "done" {
				return nil
			}
		}
	}
	return sc.Err()
}

// normalizeEvent 归一化事件名（message.delta → message 等）。
func normalizeEvent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch {
	case s == "response_done" || s == "responsedone" || s == "stream_done" || s == "streamdone":
		return "done"
	case s == "tokenusage":
		return "token_usage"
	case s == "modelconfig":
		return "model_config"
	case s == "planitem":
		return "plan_item"
	}
	return s
}

// ExtractDelta 从事件中提取文本增量（优先 message.delta，退化到常见字段）。
func ExtractDelta(ev SessionEvent) string {
	for _, path := range [][]string{
		{"delta"}, {"text"}, {"content"}, {"value"},
		{"message", "delta"}, {"message", "text"},
		{"data", "delta"}, {"data", "text"},
	} {
		if v := digString(ev.Data, path...); v != "" {
			return v
		}
	}
	return ""
}

// ExtractError 从 error 事件提取 (code, message)。
func ExtractError(ev SessionEvent) (string, string) {
	code := digString(ev.Data, "code")
	msg := digString(ev.Data, "message")
	if msg == "" {
		msg = digString(ev.Data, "error")
	}
	if msg == "" {
		msg = digString(ev.Data, "detail")
	}
	return code, msg
}

// ExtractUsage 从 token_usage 事件提取 (input, output, cached)。
func ExtractUsage(ev SessionEvent) (float64, float64, float64) {
	return digFloat(ev.Data, "input_tokens"), digFloat(ev.Data, "output_tokens"), digFloat(ev.Data, "cached_tokens")
}

func digString(m map[string]any, path ...string) string {
	v := dig(m, path...)
	s, _ := v.(string)
	return s
}

func digFloat(m map[string]any, path ...string) float64 {
	v := dig(m, path...)
	switch t := v.(type) {
	case float64:
		return t
	case string:
		return 0
	}
	return 0
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func marshalString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// StopSession 提前终止上游回合（尽力而为）。
func (u *Upstream) StopSession(ctx context.Context, token, sessionID string) {
	req, err := http.NewRequestWithContext(ctx, "POST", u.BaseURL+"/chat_sessions/"+sessionID+"/stop", bytes.NewReader([]byte("{}")))
	if err != nil {
		return
	}
	for k, v := range headers(token, false) {
		req.Header.Set(k, v)
	}
	resp, err := u.HTTP.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
