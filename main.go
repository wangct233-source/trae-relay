// trae-relay：精简版 Trae CN 反代。
// OpenAI 兼容出口 + 账号池 + 签到 + 积分 + 消费记录 + Web 控制台 + 热更新。
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"trae-relay/internal/account"
	"trae-relay/internal/admin"
	"trae-relay/internal/checkin"
	"trae-relay/internal/config"
	"trae-relay/internal/relay"
	"trae-relay/internal/updater"
	"trae-relay/internal/usage"
)

// version 由构建注入：go build -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

var defaultModels = []string{
	"auto", "glm-5.3", "glm-5.2", "deepseek-v4-pro", "kimi-k3", "doubao-seed-code",
}

func main() {
	cfg := config.Load()
	ver := version
	if v := os.Getenv("VERSION"); v != "" {
		ver = v
	}
	log.Printf("trae-relay %s 启动，监听 %s", ver, cfg.Listen)

	upstream := account.NewUpstreamClient(cfg.AuthHost, cfg.UGHost, cfg.PayHost, cfg.LoginPath)
	pool, err := account.NewPool(cfg, upstream)
	if err != nil {
		log.Fatalf("账号池初始化失败: %v", err)
	}
	tracker, err := usage.NewTracker(cfg.DataDir)
	if err != nil {
		log.Fatalf("用量存储初始化失败: %v", err)
	}
	rt, err := admin.NewRuntime(cfg.DataDir)
	if err != nil {
		log.Fatalf("设置存储初始化失败: %v", err)
	}

	up := relay.NewUpstream(cfg.UpstreamBaseURL)
	svc := &relay.Service{
		Pool:  pool,
		Up:    up,
		Usage: tracker,
		Models: splitModels(cfg.ModelsFile, defaultModels),
		CredFn: upstream.FetchCredits,
	}
	sched := checkin.New(pool, upstream, cfg)
	// 运行时设置桥接：控制台修改后立即生效
	schedSettings(sched, rt, cfg)

	binName := "trae-relay"
	if runtime.GOOS == "windows" {
		binName = "trae-relay.exe"
	}
	updates := updater.New(cfg.UpdateRepo, cfg.BinDir, binName, ver)

	// 路由
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		total, enabled := pool.Count()
		writeJSON(w, map[string]any{
			"version": ver, "accounts": total, "accounts_enabled": enabled,
			"scheduling": rt.Get().Scheduling,
		})
	})
	mux.Handle("GET /v1/models", authAPI(cfg, rt, http.HandlerFunc(svc.HandleModels)))
	mux.Handle("POST /v1/chat/completions", authAPI(cfg, rt, http.HandlerFunc(svc.HandleChat)))
	mux.Handle("POST /v1/chat", authAPI(cfg, rt, http.HandlerFunc(svc.HandleChat)))
	mux.Handle("POST /chat/completions", authAPI(cfg, rt, http.HandlerFunc(svc.HandleChat)))

	// 管理面：/admin 静态页放行（登录由页面内遮罩完成），/api/ 由 Auth 保护
	h := &admin.Handler{
		Pool: pool, Runtime: rt, Checkin: sched, Service: svc,
		Usage: tracker, Upstream: upstream, Updater: updates,
		EnvAPIKeys: cfg.APIKeys,
	}
	adminMux := http.NewServeMux()
	h.Register(adminMux)
	// 网页授权：/api/web-auth 免管理密码（回调凭证本身即授权凭据），其余 /api/ 走鉴权
	webAuth := &admin.WebAuthDeps{Pool: pool, Upstream: upstream}
	mux.HandleFunc("/web/login", webAuth.HandleLoginPage)
	mux.HandleFunc("/web/login/download", webAuth.HandleLoginDownload)
	mux.Handle("POST /api/web-auth", http.HandlerFunc(webAuth.HandleWebAuth))
	mux.HandleFunc("GET /api/models", svc.HandleModelsInfo)
	mux.Handle("GET /assets/{name}", http.HandlerFunc(admin.Asset))
	mux.Handle("/api/", admin.Auth(cfg.AdminPassword, adminMux))
	mux.HandleFunc("/admin", admin.Page)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 后台任务
	go sched.Run(ctx)
	if cfg.UpdateRepo != "" {
		go updates.Run(ctx, cfg.UpdateInterval, func() bool {
			return rt.Get().AutoUpdate
		})
	}
	// 凭证刷新巡检：每 15 分钟给临期账号换 JWT
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := pool.RefreshCredentialsAll(ctx); n > 0 {
					log.Printf("[auth] 已刷新 %d 个账号的 JWT", n)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr: cfg.Listen, Handler: mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shtd, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shtd)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
	log.Println("trae-relay 已停止")
}

// authAPI OpenAI 出口鉴权：env API_KEYS 与控制台运行时 APIKeys 合并生效；
// 两者皆为空则放行（建议内网部署或尽快在控制台配置 Key）。
func authAPI(cfg *config.Config, rt *admin.Runtime, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := append([]string{}, cfg.APIKeys...)
		keys = append(keys, rt.Get().APIKeys...)
		if len(keys) > 0 {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			ok := false
			for _, k := range keys {
				if key == k {
					ok = true
					break
				}
			}
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth_error"}}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// schedSettings 让签到调度器读取控制台运行时开关。
func schedSettings(s *checkin.Scheduler, rt *admin.Runtime, cfg *config.Config) {
	// Scheduler.Cfg 是启动配置；这里把运行时覆盖写回（每分钟同步一次开销可忽略）
	go func() {
		for {
			st := rt.Get()
			s.Cfg.AutoCheckin = st.AutoCheckin
			s.Cfg.CheckinTime = st.CheckinTime
			time.Sleep(30 * time.Second)
		}
	}()
}

// splitModels 模型列表：优先环境变量逗号分隔，否则内置默认。
func splitModels(file string, def []string) []string {
	if v := os.Getenv("MODELS"); v != "" {
		var out []string
		for _, m := range strings.Split(v, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	_ = file
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
