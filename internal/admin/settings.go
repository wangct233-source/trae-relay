package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"trae-relay/internal/store"
)

// Settings 运行时可变设置（控制台修改即时生效，持久化到 settings.json）。
type Settings struct {
	Scheduling  string   `json:"scheduling"`     // round-robin | credit
	AutoCheckin bool     `json:"auto_checkin"`
	CheckinTime string   `json:"checkin_time"`
	AutoUpdate  bool     `json:"auto_update"`
	APIKeys     []string `json:"api_keys"`       // 对话 API 密钥（运行时可配，与 env 合并生效）
}

// Runtime 全局运行时设置句柄。
type Runtime struct {
	file *store.JSONFile[Settings]
	mu   sync.Mutex
}

func NewRuntime(dataDir string) (*Runtime, error) {
	f, err := store.NewJSONFile[Settings](dataDir, "settings.json", Settings{
		Scheduling: "round-robin", AutoCheckin: true, CheckinTime: "08:30",
	})
	return &Runtime{file: f}, err
}

func (r *Runtime) Get() Settings { return r.file.Get() }

func (r *Runtime) Set(s Settings) error {
	return r.file.Update(func(cur *Settings) { *cur = s })
}

// Auth 中间件：管理接口鉴权（密码为空时仅在无 X-Forwarded-For 的内网放行由部署者自行保证）。
func Auth(password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if password != "" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				token = r.URL.Query().Get("key")
			}
			if token != password {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// JSON 响应工具。
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func body(r *http.Request) (map[string]any, error) {
	var m map[string]any
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&m)
	return m, err
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func boolean(m map[string]any, key string) (bool, bool) {
	v, ok := m[key].(bool)
	return v, ok
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
