// Package config 集中管理环境变量配置。
package config

import (
	"os"
	"strconv"
	"time"
)

// Config 持有全部运行配置，进程启动时解析一次。
type Config struct {
	Listen        string // HTTP 监听地址
	APIKeys       []string
	DataDir       string
	BinDir        string // 热更新二进制所在目录（Docker 中挂载卷）
	AdminPassword string // 控制台密码；为空则仅监听内网时免鉴权

	UpstreamBaseURL string // remote chat_sessions 上游
	AuthHost        string // ExchangeToken / GetUserInfo
	UGHost          string // 签到
	PayHost         string // 积分权益
	LoginPath       string // 密码登录端点（可按上游变更覆盖）

	SlotPerAccount     int           // 每账号并发槽位
	SlotQueueTimeout   time.Duration // 排队超时
	CooldownOnErr      int           // 连续 N 次错误进入冷却
	CooldownSeconds    int
	IdleSessionReapSec int

	CheckinTime     string // 每日签到时间 HH:MM（北京时间）
	CheckinInterval time.Duration
	CheckinBackoff  time.Duration // 9074 首次退避
	BackoffMax      time.Duration
	AutoCheckin     bool

	AutoUpdate     bool
	UpdateInterval time.Duration
	UpdateRepo     string // GitHub owner/repo，用于热更新

	ModelsFile string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off", "":
		return def
	}
	return def
}

func envSeconds(key string, def int) time.Duration {
	return time.Duration(envInt(key, def)) * time.Second
}

// Load 从环境变量读取配置。
func Load() *Config {
	c := &Config{
		Listen:             env("LISTEN", ":8080"),
		DataDir:            env("DATA_DIR", "data"),
		BinDir:             env("BIN_DIR", "."),
		AdminPassword:      env("ADMIN_PASSWORD", ""),
		UpstreamBaseURL:    env("TRAE_UPSTREAM_BASE", "https://trae-api-cn.mchost.guru/api/remote/v1"),
		AuthHost:           env("TRAE_AUTH_HOST", "https://api.trae.cn"),
		UGHost:             env("TRAE_UG_HOST", "https://api.trae.cn"),
		PayHost:            env("TRAE_PAY_HOST", "https://api.trae.cn"),
		LoginPath:          env("TRAE_LOGIN_PATH", "/cloudide/api/v3/trae/oauth/PasswordLogin"),
		SlotPerAccount:     envInt("SLOT_PER_ACCOUNT", 2),
		SlotQueueTimeout:   envSeconds("SLOT_QUEUE_TIMEOUT", 60),
		CooldownOnErr:      envInt("COOLDOWN_ON_ERR", 3),
		CooldownSeconds:    envInt("COOLDOWN_SECONDS", 300),
		IdleSessionReapSec: envInt("IDLE_REAP_SECONDS", 60),
		CheckinTime:        env("CHECKIN_TIME", "08:30"),
		CheckinInterval:    envSeconds("CHECKIN_INTERVAL", 60),
		CheckinBackoff:     envSeconds("CHECKIN_BACKOFF", 60),
		BackoffMax:         envSeconds("BACKOFF_MAX", 3600),
		AutoCheckin:        envBool("AUTO_CHECKIN", true),
		AutoUpdate:         envBool("AUTO_UPDATE", false),
		UpdateInterval:     envSeconds("UPDATE_CHECK_INTERVAL", 3600),
		UpdateRepo:         env("UPDATE_REPO", ""),
		ModelsFile:         env("MODELS_FILE", ""),
	}
	for _, k := range splitComma(env("API_KEYS", "")) {
		c.APIKeys = append(c.APIKeys, k)
	}
	return c
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' || r == '，' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
