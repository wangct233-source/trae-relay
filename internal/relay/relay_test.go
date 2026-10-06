package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"trae-relay/internal/account"
	"trae-relay/internal/config"
	"trae-relay/internal/usage"
)

// TestChatEndToEnd 用假上游验证 创建会话→读事件→OpenAI 输出 全链路。
func TestChatEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/chat_sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"chat_session_id": "s1", "message_id": "m1"},
			})
		case r.URL.Path == "/chat_sessions/s1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			for _, ev := range []string{
				"event: model_config\ndata: {\"model\":\"glm-5.3\"}\n\n",
				"event: message\ndata: {\"delta\":\"你好\"}\n\n",
				"event: message\ndata: {\"delta\":\"，世界\"}\n\n",
				"event: token_usage\ndata: {\"input_tokens\":10,\"output_tokens\":5}\n\n",
				"event: done\ndata: {}\n\n",
			} {
				_, _ = w.Write([]byte(ev))
				fl.Flush()
			}
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, SlotPerAccount: 1, SlotQueueTimeout: 5, CooldownOnErr: 3}
	pool, err := account.NewPool(cfg, account.NewUpstreamClient("http://x", "http://x", "http://x", "/x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pool.ImportJSON(`{"token":"fake.jwt.token","user_id":"u1"}`); err != nil {
		t.Fatal(err)
	}
	tracker, err := usage.NewTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Pool: pool, Up: NewUpstream(up.URL), Usage: tracker, Models: []string{"auto"}}

	body, _ := json.Marshal(map[string]any{
		"model": "auto",
		"messages": []map[string]any{
			{"role": "system", "content": "你是助手"},
			{"role": "user", "content": "打招呼"},
		},
		"stream": false,
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	svc.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "你好，世界" {
		t.Fatalf("content = %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	// 消费记录落盘
	recs := tracker.Records(10)
	if len(recs) != 1 || recs[0].Status != "ok" || recs[0].Input != 10 {
		t.Fatalf("usage record = %+v", recs)
	}
}

// TestStreamChat 流式输出应产出 role chunk、内容 chunk、finish、[DONE]。
func TestStreamChat(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat_sessions" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"chat_session_id": "s1", "message_id": "m1"},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message\ndata: {\"delta\":\"Hi\"}\n\nevent: done\ndata: {}\n\n"))
		fl.Flush()
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, SlotPerAccount: 1, SlotQueueTimeout: 5}
	pool, _ := account.NewPool(cfg, account.NewUpstreamClient("http://x", "http://x", "http://x", "/x"))
	_, _, _ = pool.ImportJSON(`{"token":"fake.jwt.token","user_id":"u1"}`)
	tracker, _ := usage.NewTracker(dir)
	svc := &Service{Pool: pool, Up: NewUpstream(up.URL), Usage: tracker, Models: []string{"auto"}}

	body, _ := json.Marshal(map[string]any{
		"model": "auto", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	svc.HandleChat(rec, req)
	out := rec.Body.String()
	for _, want := range []string{"\"role\":\"assistant\"", "\"content\":\"Hi\"", "\"finish_reason\":\"stop\"", "data: [DONE]"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("stream output missing %s:\n%s", want, out)
		}
	}
}
