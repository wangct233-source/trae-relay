package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"trae-relay/internal/account"
	"trae-relay/internal/usage"
)

// Service OpenAI 兼容出口：/v1/models、/v1/chat/completions。
type Service struct {
	Pool   *account.Pool
	Up     *Upstream
	Usage  *usage.Tracker
	Models []string
	// CredFn 积分查询函数（由 main 装配，指向 account.UpstreamClient.FetchCredits）
	CredFn func(ctx context.Context, token, userID string) (remaining, total float64, unlimited, ok bool, err error)
}

// ChatRequest OpenAI Chat Completions 请求（只取需要的字段）。
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": code, "code": code},
	})
}

// ModelInfo 模型展示信息（倍率与上下文为相对参考值）。
type ModelInfo struct {
	ID      string  `json:"id"`
	Ratio   float64 `json:"ratio"`   // 计费倍率（相对基准 1×）
	Context int     `json:"context"` // 上下文窗口（tokens）
}

// defaultModelInfos 内置模型展示表；env MODELS 追加的项按 1×/128K 处理。
var defaultModelInfos = map[string]ModelInfo{
	"auto":             {ID: "auto", Ratio: 1, Context: 1000000},
	"glm-5.3":          {ID: "glm-5.3", Ratio: 1, Context: 128000},
	"glm-5.2":          {ID: "glm-5.2", Ratio: 1, Context: 128000},
	"deepseek-v4-pro":  {ID: "deepseek-v4-pro", Ratio: 1, Context: 128000},
	"kimi-k3":          {ID: "kimi-k3", Ratio: 1, Context: 256000},
	"doubao-seed-code": {ID: "doubao-seed-code", Ratio: 1, Context: 128000},
}

// ModelInfos 返回完整模型展示表（内置 + env 追加）。
func (s *Service) ModelInfos() []ModelInfo {
	seen := map[string]bool{}
	out := make([]ModelInfo, 0, len(s.Models))
	for _, id := range s.Models {
		if seen[id] {
			continue
		}
		seen[id] = true
		if mi, ok := defaultModelInfos[id]; ok {
			out = append(out, mi)
		} else {
			out = append(out, ModelInfo{ID: id, Ratio: 1, Context: 128000})
		}
	}
	return out
}

// HandleModelsInfo GET /api/models —— 控制台模型列表（含倍率与上下文）。
func (s *Service) HandleModelsInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"models": s.ModelInfos()})
}

// HandleModels GET /v1/models
func (s *Service) HandleModels(w http.ResponseWriter, r *http.Request) {
	models := s.Models
	if len(models) == 0 {
		models = []string{"auto"}
	}
	list := make([]map[string]any, 0, len(models))
	for _, m := range models {
		list = append(list, map[string]any{
			"id": m, "object": "model", "owned_by": "trae-relay",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
}

// HandleChat POST /v1/chat/completions（流式与非流式）。
func (s *Service) HandleChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid_request_error", "请求体解析失败: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, 400, "invalid_request_error", "messages 不能为空")
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "auto"
	}

	ctx := r.Context()
	scheduling := envString("SCHEDULING", "round-robin")
	lease, err := s.Pool.Acquire(ctx, scheduling)
	if err != nil {
		writeErr(w, 503, "no_account_available", err.Error())
		return
	}
	defer lease.Release()
	acc := lease.Account

	recID, commit := s.Usage.Start(acc.ID, model)
	input, output, cached := 0.0, 0.0, 0.0
	commitFn := func(status, errMsg string) {
		total := input + output
		commit(usage.Record{
			AccountID: acc.ID, Model: model,
			Input: input, Output: output, Cached: cached, Total: total,
			Status: status, Error: errMsg,
		})
	}

	query := FlattenQuery(req.Messages)
	if query == "" {
		commitFn("error", "消息内容为空")
		writeErr(w, 400, "invalid_request_error", "消息内容为空")
		return
	}
	sessionID := StableSessionID(acc.ID, model)
	sid, mid, err := s.Up.CreateSession(ctx, acc.Token, model, query, sessionID)
	if err != nil {
		s.Pool.ReportError(acc.ID, err.Error())
		commitFn("error", err.Error())
		writeErr(w, 502, "upstream_error", "创建上游会话失败: "+err.Error())
		return
	}
	defer func() {
		if ctx.Err() != nil {
			go s.Up.StopSession(context.Background(), acc.Token, sid)
		}
	}()

	full := strings.Builder{}
	errCh := make(chan error, 1)
	eventDone := make(chan struct{})
	var streamW *streamWriter

	if req.Stream {
		streamW = newStreamWriter(w, model)
		if err := streamW.open(); err != nil {
			commitFn("error", "客户端连接已断开")
			return
		}
	}

	consume := func(ev SessionEvent) error {
		switch ev.Name {
		case "error":
			code, msg := ExtractError(ev)
			return fmt.Errorf("上游错误 %s: %s", code, msg)
		case "token_usage":
			input, output, cached = ExtractUsage(ev)
		case "message", "assistant_message", "response", "text", "output", "plan_item", "model_config":
			if delta := ExtractDelta(ev); delta != "" {
				full.WriteString(delta)
				if streamW != nil {
					return streamW.sendDelta(delta)
				}
			}
		}
		return nil
	}

	go func() {
		defer close(eventDone)
		errCh <- s.Up.StreamEvents(ctx, acc.Token, sid, mid, consume)
	}()
	err = <-errCh

	if err != nil {
		s.Pool.ReportError(acc.ID, err.Error())
		commitFn("error", err.Error())
		if streamW != nil {
			_ = streamW.sendError(err.Error())
		} else {
			writeErr(w, 502, "upstream_error", "上游事件流失败: "+err.Error())
		}
		return
	}
	if full.Len() == 0 {
		s.Pool.ReportError(acc.ID, "空响应")
		commitFn("error", "上游返回空响应")
		if streamW != nil {
			_ = streamW.sendError("上游返回空响应")
		} else {
			writeErr(w, 502, "upstream_error", "上游返回空响应")
		}
		return
	}

	s.Pool.ReportSuccess(acc.ID)
	text := full.String()
	if streamW != nil {
		_ = streamW.finish()
	} else {
		respBody := map[string]any{
			"id": "chatcmpl-" + recID, "object": "chat.completion",
			"created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": int(input), "completion_tokens": int(output),
				"total_tokens": int(input + output),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respBody)
	}
	commitFn("ok", "")
	// 异步做积分快照刷新（积分优先调度的数据源）
	go s.refreshCredits(acc.ID, acc.Token, acc.UserID)
}

// refreshCredits 请求结束后刷新账号积分快照（尽力而为）。
func (s *Service) refreshCredits(accID, token, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.CredFn == nil {
		return
	}
	remaining, total, unlimited, ok, err := s.CredFn(ctx, token, userID)
	if err == nil && ok {
		s.Pool.UpdateCredits(accID, remaining, total, unlimited)
	}
}
