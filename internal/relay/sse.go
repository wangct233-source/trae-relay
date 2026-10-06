package relay

import (
	"encoding/json"
	"net/http"
	"time"
)

// streamWriter OpenAI Chat Completions SSE 输出，带空闲心跳注释帧。
type streamWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	model   string
	id      string
	created int64
	index   int
	err     error
}

func newStreamWriter(w http.ResponseWriter, model string) *streamWriter {
	flusher, _ := w.(http.Flusher)
	return &streamWriter{
		w: w, flusher: flusher, model: model,
		id:      "chatcmpl-" + time.Now().Format("20060102150405"),
		created: time.Now().Unix(),
	}
}

func (s *streamWriter) open() error {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(200)
	s.flusher.Flush()
	// role 先行 chunk
	return s.write(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created,
		"model": s.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": ""},
		}},
	})
}

func (s *streamWriter) sendDelta(text string) error {
	if s.err != nil {
		return s.err
	}
	return s.write(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created,
		"model": s.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"content": text},
		}},
	})
}

func (s *streamWriter) finish() error {
	if s.err != nil {
		return s.err
	}
	if err := s.write(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created,
		"model": s.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}},
	}); err != nil {
		return err
	}
	_, err := s.w.Write([]byte("data: [DONE]\n\n"))
	if s.flusher != nil {
		s.flusher.Flush()
	}
	s.err = err
	return err
}

// sendError 以 OpenAI error 事件收尾（流已开始后只能流内报错）。
func (s *streamWriter) sendError(msg string) error {
	if s.err != nil {
		return s.err
	}
	_ = s.write(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created,
		"model": s.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}},
		"error": map[string]any{"message": msg, "type": "upstream_error"},
	})
	_, _ = s.w.Write([]byte("data: [DONE]\n\n"))
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

func (s *streamWriter) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := s.w.Write(append([]byte("data: "), append(raw, '\n', '\n')...)); err != nil {
		s.err = err
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}
