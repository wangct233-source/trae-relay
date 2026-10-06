package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"trae-relay/internal/config"
	"trae-relay/internal/store"
)

// Pool 账号池：持久化、批量导入、启停、轮询/积分优先调度、并发槽位、冷却。
type Pool struct {
	file   *store.JSONFile[map[string]*Account]
	client *UpstreamClient
	cfg    *config.Config

	mu    sync.Mutex
	slots map[string]chan struct{}
}

var (
	ErrNoAccount   = errors.New("没有可用账号：请先导入账号")
	ErrAllCooldown = errors.New("所有账号已禁用或冷却中")
	ErrSlotTimeout = errors.New("账号并发槽位已满且排队超时")
)

func NewPool(cfg *config.Config, client *UpstreamClient) (*Pool, error) {
	f, err := store.NewJSONFile[map[string]*Account](cfg.DataDir, "accounts.json", map[string]*Account{})
	if err != nil {
		return nil, err
	}
	p := &Pool{file: f, client: client, cfg: cfg, slots: map[string]chan struct{}{}}
	// 首次启动时补齐字段
	_ = p.file.Update(func(m *map[string]*Account) {
		for id, a := range *m {
			if a.ID == "" {
				a.ID = id
			}
		}
	})
	return p, nil
}

// List 返回全部账号快照。
func (p *Pool) List() []Account {
	m := p.file.Get()
	out := make([]Account, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Count 返回 (总数, 启用数)。
func (p *Pool) Count() (int, int) {
	m := p.file.Get()
	total, enabled := 0, 0
	for _, a := range m {
		total++
		if a.Enabled {
			enabled++
		}
	}
	return total, enabled
}

// SetEnabled 启用/禁用账号。
func (p *Pool) SetEnabled(id string, enabled bool) error {
	return p.file.Update(func(m *map[string]*Account) {
		if a, ok := (*m)[id]; ok {
			a.Enabled = enabled
			if enabled {
				a.ConsecErrors = 0
				a.CooldownUntil = 0
			}
		}
	})
}

// Remove 删除账号。
func (p *Pool) Remove(id string) error {
	p.mu.Lock()
	delete(p.slots, id)
	p.mu.Unlock()
	return p.file.Update(func(m *map[string]*Account) { delete(*m, id) })
}

// Patch 更新账号备注等字段。
func (p *Pool) Patch(id string, label *string) error {
	return p.file.Update(func(m *map[string]*Account) {
		if a, ok := (*m)[id]; ok && label != nil {
			a.Label = *label
		}
	})
}

// ImportJSON 批量导入账号。
// 支持单个对象或数组，字段兼容 token/accessToken、refresh_token/refreshToken、
// user_id/userId；同 ID（或同 user_id）覆盖更新，其余新增。
func (p *Pool) ImportJSON(text string) (added, updated int, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, errors.New("导入内容为空")
	}
	var items []map[string]any
	if strings.HasPrefix(text, "[") {
		var raw []any
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return 0, 0, fmt.Errorf("JSON 数组解析失败: %w", err)
		}
		for _, it := range raw {
			if m, ok := it.(map[string]any); ok {
				items = append(items, m)
			}
		}
	} else {
		one := map[string]any{}
		if err := json.Unmarshal([]byte(text), &one); err != nil {
			return 0, 0, fmt.Errorf("JSON 解析失败: %w", err)
		}
		items = []map[string]any{one}
	}
	if len(items) == 0 {
		return 0, 0, errors.New("没有可导入的账号")
	}
	err = p.file.Update(func(m *map[string]*Account) {
		for _, item := range items {
			acc := normalizeImport(item)
			if acc.Token == "" && acc.RefreshToken == "" {
				continue
			}
			if acc.ID == "" && acc.UserID != "" {
				acc.ID = acc.UserID
			}
			if acc.ID == "" {
				acc.ID = fmt.Sprintf("acc-%d", time.Now().UnixNano())
			}
			if old, ok := (*m)[acc.ID]; ok {
				acc.mergeFrom(old)
				(*m)[acc.ID] = acc
				updated++
			} else {
				(*m)[acc.ID] = acc
				added++
			}
		}
	})
	return added, updated, err
}

// ImportPassword 用账号密码登录上游并导入返回的凭证。
func (p *Pool) ImportPassword(ctx context.Context, phone, password, captcha string) (*Account, error) {
	refresh, userID, err := p.client.PasswordLogin(ctx, phone, password, captcha)
	if err != nil {
		return nil, err
	}
	acc := &Account{
		ID: userID, UserID: userID, RefreshToken: refresh,
		Enabled: true, CreatedAt: time.Now().Unix(),
	}
	if userID != "" {
		// 立即用 RefreshToken 换取 JWT
		clientID := "ono9krqynydwx5"
		if token, expiry, uid, err := p.client.ExchangeToken(ctx, refresh, clientID); err == nil {
			acc.Token, acc.TokenExpiry, acc.UserID = token, expiry, uid
			if acc.ID == "" {
				acc.ID = uid
			}
		}
	}
	if acc.ID == "" {
		acc.ID = fmt.Sprintf("acc-%d", time.Now().UnixNano())
	}
	err = p.file.Update(func(m *map[string]*Account) {
		if old, ok := (*m)[acc.ID]; ok {
			acc.mergeFrom(old)
		}
		(*m)[acc.ID] = acc
	})
	return acc, err
}

func (a *Account) mergeFrom(old *Account) {
	if a.Token == "" {
		a.Token = old.Token
	}
	if a.TokenExpiry == 0 {
		a.TokenExpiry = old.TokenExpiry
	}
	if a.UserID == "" {
		a.UserID = old.UserID
	}
	if a.Label == "" {
		a.Label = old.Label
	}
	a.ConsecErrors = old.ConsecErrors
	a.CooldownUntil = old.CooldownUntil
	a.CheckinGen = old.CheckinGen
	a.LastCheckinAt = old.LastCheckinAt
	a.LastCheckinMsg = old.LastCheckinMsg
	a.CreditsRemaining = old.CreditsRemaining
	a.CreditsTotal = old.CreditsTotal
	a.CreditsUnlimited = old.CreditsUnlimited
	a.CreatedAt = old.CreatedAt
}

// normalizeImport 宽松映射导入字段。
func normalizeImport(item map[string]any) *Account {
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := item[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	a := &Account{
		ID:           str("id", "ID"),
		Label:        str("label", "name", "备注"),
		UserID:       str("user_id", "userId", "UserID"),
		Token:        str("token", "accessToken", "access_token", "jwt", "Token"),
		RefreshToken: str("refresh_token", "refreshToken", "RefreshToken"),
		Region:       str("region", "Region"),
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
	}
	if a.Token != "" {
		a.TokenExpiry = JWTExpiry(a.Token)
		if a.UserID == "" {
			a.UserID = JWTUser(a.Token)
		}
	}
	if e, ok := item["enabled"].(bool); ok {
		a.Enabled = e
	}
	return a
}

// Lease 一次账号租约：持有一个并发槽位。
type Lease struct {
	Account *Account
	pool    *Pool
	releaseOnce sync.Once
}

// Release 归还槽位（非阻塞：账号被删除后通道不存在时直接丢弃）。
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		select {
		case l.pool.muSlot(l.Account.ID) <- struct{}{}:
		default:
		}
	})
}

// Acquire 选择一个可用账号并占用其并发槽位。
// mode: "round-robin" 或 "credit"（积分优先）。
func (p *Pool) Acquire(ctx context.Context, mode string) (*Lease, error) {
	m := p.file.Get()
	var candidates []*Account
	for _, a := range m {
		if a.Active() && a.TokenValid() {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		if len(m) == 0 {
			return nil, ErrNoAccount
		}
		return nil, ErrAllCooldown
	}
	var picked *Account
	if mode == "credit" {
		// 积分多者优先；无积分数据者排后
		sort.Slice(candidates, func(i, j int) bool {
			return accountCreditKey(candidates[i]) > accountCreditKey(candidates[j])
		})
		picked = candidates[0]
	} else {
		// 顺序轮询：取 last_used_at 最小者
		picked = candidates[0]
		for _, a := range candidates[1:] {
			if a.LastUsedAt < picked.LastUsedAt {
				picked = a
			}
		}
	}

	ch := p.muSlot(picked.ID)
	timer := time.NewTimer(p.cfg.SlotQueueTimeout)
	defer timer.Stop()
	select {
	case _, ok := <-ch:
		if !ok {
			return nil, ErrSlotTimeout
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrSlotTimeout
	}
	_ = p.file.Update(func(m *map[string]*Account) {
		if a, ok := (*m)[picked.ID]; ok {
			a.LastUsedAt = time.Now().Unix()
		}
	})
	return &Lease{Account: picked, pool: p}, nil
}

func accountCreditKey(a *Account) float64 {
	if a.CreditsUnlimited {
		return 1e18
	}
	if a.CreditsRemaining != nil {
		return *a.CreditsRemaining
	}
	return 0
}

// muSlot 取账号槽位通道（容量 = SLOT_PER_ACCOUNT），取走令牌即占坑。
func (p *Pool) muSlot(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, ok := p.slots[id]
	if !ok {
		n := p.cfg.SlotPerAccount
		if n < 1 {
			n = 1
		}
		ch = make(chan struct{}, n)
		for i := 0; i < n; i++ {
			ch <- struct{}{}
		}
		p.slots[id] = ch
	}
	return ch
}

// ReportSuccess 请求成功：清零错误计数。
func (p *Pool) ReportSuccess(id string) {
	_ = p.file.Update(func(m *map[string]*Account) {
		if a, ok := (*m)[id]; ok {
			a.ConsecErrors = 0
		}
	})
}

// ReportError 请求失败：连续错误达到阈值进入冷却。
func (p *Pool) ReportError(id string, reason string) {
	_ = p.file.Update(func(m *map[string]*Account) {
		a, ok := (*m)[id]
		if !ok {
			return
		}
		a.ConsecErrors++
		if a.ConsecErrors >= p.cfg.CooldownOnErr {
			a.CooldownUntil = time.Now().Add(time.Duration(p.cfg.CooldownSeconds) * time.Second).Unix()
			a.ConsecErrors = 0
		}
	})
}

// TryRefreshToken 用 RefreshToken 刷新 JWT（即将过期时调用）。
func (p *Pool) TryRefreshToken(ctx context.Context, id string) bool {
	a := p.get(id)
	if a == nil || a.RefreshToken == "" {
		return false
	}
	token, expiry, uid, err := p.client.ExchangeToken(ctx, a.RefreshToken, "ono9krqynydwx5")
	if err != nil {
		return false
	}
	return p.file.Update(func(m *map[string]*Account) {
		if cur, ok := (*m)[id]; ok {
			cur.Token, cur.TokenExpiry = token, expiry
			if uid != "" {
				cur.UserID = uid
			}
		}
	}) == nil
}

// RecordCheckin 写回签到结果；rotateDevice=true 时前移设备代际（应对 9074）。
func (p *Pool) RecordCheckin(id string, msg string, rotateDevice bool) {
	_ = p.file.Update(func(m *map[string]*Account) {
		a, ok := (*m)[id]
		if !ok {
			return
		}
		a.LastCheckinAt = time.Now().Unix()
		a.LastCheckinMsg = msg
		if rotateDevice {
			a.CheckinGen++
		}
	})
}

// UpdateCredits 写回积分查询结果。
func (p *Pool) UpdateCredits(id string, remaining, total float64, unlimited bool) {
	_ = p.file.Update(func(m *map[string]*Account) {
		if a, ok := (*m)[id]; ok {
			a.CreditsQueriedAt = time.Now().Unix()
			a.CreditsUnlimited = unlimited
			if !unlimited {
				r, t := remaining, total
				a.CreditsRemaining, a.CreditsTotal = &r, &t
			}
		}
	})
}

// RefreshCredentialsAll 对临近过期的账号尝试用 RefreshToken 换新 JWT。
func (p *Pool) RefreshCredentialsAll(ctx context.Context) int {
	n := 0
	for _, a := range p.List() {
		if a.RefreshToken != "" && !a.TokenValid() && a.Enabled {
			if p.TryRefreshToken(ctx, a.ID) {
				n++
			}
		}
	}
	return n
}

func (p *Pool) get(id string) *Account {
	m := p.file.Get()
	return m[id]
}
