// Package account 定义账号模型、Trae 上游 API 客户端与账号池调度。
package account

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Account 一个 Trae 账号凭证与运行状态。
type Account struct {
	ID          string  `json:"id"`                    // 稳定标识（user id 或派生哈希）
	Label       string  `json:"label,omitempty"`       // 备注名
	UserID      string  `json:"user_id,omitempty"`     // 上游 user id
	Token       string  `json:"token"`                 // Cloud-IDE-JWT
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenExpiry int64   `json:"token_expiry,omitempty"` // unix 秒
	Region      string  `json:"region,omitempty"`       // CN / SG
	Enabled     bool    `json:"enabled"`
	Priority    int     `json:"priority,omitempty"` // 积分优先调度时的排序权重缓存

	// 运行状态（随 accounts.json 持久化）
	ConsecErrors int    `json:"consec_errors"`
	CooldownUntil int64 `json:"cooldown_until,omitempty"`
	LastUsedAt   int64  `json:"last_used_at,omitempty"`
	LastCheckinAt int64 `json:"last_checkin_at,omitempty"`
	LastCheckinMsg string `json:"last_checkin_msg,omitempty"`
	CheckinGen   int    `json:"checkin_gen,omitempty"` // 9074 设备代际
	CreditsRemaining *float64 `json:"credits_remaining,omitempty"`
	CreditsTotal     *float64 `json:"credits_total,omitempty"`
	CreditsUnlimited bool     `json:"credits_unlimited,omitempty"`
	CreditsQueriedAt int64    `json:"credits_queried_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// Active 判断账号当前是否可被调度。
func (a *Account) Active() bool {
	return a.Enabled && time.Now().Unix() >= a.CooldownUntil
}

// TokenValid 判断 JWT 是否临近过期（5 分钟缓冲）。
func (a *Account) TokenValid() bool {
	if a.Token == "" {
		return false
	}
	if a.TokenExpiry == 0 {
		return true
	}
	return a.TokenExpiry-time.Now().Unix() > 300
}

// JWTUser 从 JWT payload 提取稳定用户标识（data.id / user_id / sub）。
func JWTUser(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1] + strings.Repeat("=", (4-len(parts[1])%4)%4))
		if err != nil {
			return ""
		}
	}
	var payload struct {
		Data   map[string]any `json:"data"`
		UserID any            `json:"user_id"`
		Sub    any            `json:"sub"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if payload.Data != nil {
		if id, ok := payload.Data["id"].(string); ok && id != "" {
			return id
		}
	}
	for _, v := range []any{payload.UserID, payload.Sub} {
		if s := anyToString(v); s != "" {
			return s
		}
	}
	return ""
}

// JWTExpiry 提取 JWT exp（unix 秒），缺失返回 0。
func JWTExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return 0
	}
	var payload struct {
		Exp any `json:"exp"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return 0
	}
	switch v := payload.Exp.(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		if n > 1e12 {
			n /= 1000
		}
		return n
	}
	return 0
}

// CheckinDeviceID 派生账号绑定的 16 位签到设备 ID。
// 上游 9074 风控按设备 ID 限流；gen 代际前移可换到全新设备 ID。
func CheckinDeviceID(identity string, gen int) string {
	material := identity
	if gen > 0 {
		material = fmt.Sprintf("%s#gen%d", identity, gen)
	}
	sum := sha256.Sum256([]byte(material))
	n := new(uint64)
	for _, b := range sum[:8] {
		*n = *n<<8 | uint64(b)
	}
	return fmt.Sprintf("%016d", *n%1e16)
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	}
	return ""
}
