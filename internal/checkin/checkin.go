// Package checkin 每日定时签到 + 9074 风控设备轮换退避重试。
package checkin

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"trae-relay/internal/account"
	"trae-relay/internal/config"
)

// Result 单账号签到结果。
type Result struct {
	AccountID string `json:"account_id"`
	OK        bool   `json:"ok"`
	Checked   bool   `json:"already_checked"`
	Message   string `json:"message"`
}

// Scheduler 签到调度器。
type Scheduler struct {
	Pool   *account.Pool
	Client *account.UpstreamClient
	Cfg    *config.Config

	mu       sync.Mutex
	backoff  map[string]time.Time // 9074 退避到期时间
	lastDay  string               // 上次完成全天签到的日期（北京）
	lastRun  time.Time
	lastAuto string // 上次自动签到结果摘要
}

func New(pool *account.Pool, client *account.UpstreamClient, cfg *config.Config) *Scheduler {
	return &Scheduler{Pool: pool, Client: client, Cfg: cfg, backoff: map[string]time.Time{}}
}

func deviceID(a account.Account) string {
	identity := a.UserID
	if identity == "" {
		identity = a.ID
	}
	return account.CheckinDeviceID(identity, a.CheckinGen)
}

// CheckinOne 对单个账号执行签到。
func (s *Scheduler) CheckinOne(ctx context.Context, a account.Account) Result {
	res := Result{AccountID: a.ID}
	did := deviceID(a)
	claim, err := s.Client.CheckinClaim(ctx, a.Token, did)
	if err != nil {
		msg := err.Error()
		// 9074：设备维度限流。前移设备代际，安排退避重试。
		if strings.Contains(msg, "9074") {
			s.Pool.RecordCheckin(a.ID, "9074 风控，设备已轮换待重试", true)
			s.setBackoff(a.ID, s.Cfg.CheckinBackoff)
			res.Message = "9074 风控：已轮换设备 ID，稍后自动重试"
			return res
		}
		s.Pool.RecordCheckin(a.ID, msg, false)
		res.Message = msg
		return res
	}
	code := jsonFloat(claim, "code")
	switch {
	case code == 0:
		credits := ""
		if v := jsonAny(claim, "credits"); v != nil {
			credits = fmt.Sprintf("（+%v）", v)
		}
		s.Pool.RecordCheckin(a.ID, "签到成功"+credits, false)
		res.OK = true
		res.Message = "签到成功" + credits
	case code == 9074:
		s.Pool.RecordCheckin(a.ID, "9074 风控，设备已轮换待重试", true)
		s.setBackoff(a.ID, s.Cfg.CheckinBackoff)
		res.Message = "9074 风控：已轮换设备 ID，稍后自动重试"
	default:
		msg := fmt.Sprintf("业务码 %v: %v", code, jsonAny(claim, "message"))
		s.Pool.RecordCheckin(a.ID, msg, false)
		res.Message = msg
	}
	return res
}

// CheckinAll 顺序签到全部启用账号（账号间隔 CHECKIN_INTERVAL）。
func (s *Scheduler) CheckinAll(ctx context.Context) []Result {
	var out []Result
	for _, a := range s.Pool.List() {
		if !a.Enabled {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		out = append(out, s.CheckinOne(ctx, a))
		if len(out) > 0 {
			select {
			case <-time.After(s.Cfg.CheckinInterval):
			case <-ctx.Done():
				return out
			}
		}
	}
	return out
}

// Run 后台主循环：每日定时签到 + 9074 退避账号扫描重试。
func (s *Scheduler) Run(ctx context.Context) {
	for {
		now := beijingNow()
		if s.Cfg.AutoCheckin && s.dailyDue(now) {
			log.Printf("[checkin] 开始每日定时签到 (%s)", s.Cfg.CheckinTime)
			results := s.CheckinAll(ctx)
			okCount := 0
			for _, r := range results {
				if r.OK {
					okCount++
				}
			}
			s.mu.Lock()
			s.lastDay = now.Format("2006-01-02")
			s.lastRun = time.Now()
			s.lastAuto = fmt.Sprintf("%d/%d 成功", okCount, len(results))
			s.mu.Unlock()
			log.Printf("[checkin] 定时签到完成: %s", s.lastAuto)
		}
		// 退避重试扫描
		s.retryBackoff(ctx, now)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.Cfg.CheckinInterval):
		}
	}
}

// Status 返回调度器状态摘要（控制台展示）。
func (s *Scheduler) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.nextRun(beijingNow())
	return map[string]any{
		"enabled":    s.Cfg.AutoCheckin,
		"time":       s.Cfg.CheckinTime,
		"last_day":   s.lastDay,
		"last_run":   s.lastRun.Format("2006-01-02 15:04:05"),
		"last_auto":  s.lastAuto,
		"next_run":   next.Format("2006-01-02 15:04:05"),
		"pending":    len(s.backoff),
	}
}

func (s *Scheduler) nextRun(now time.Time) time.Time {
	target := s.Cfg.CheckinTime
	var hh, mm int
	if _, err := fmt.Sscanf(target, "%02d:%02d", &hh, &mm); err != nil {
		hh, mm = 8, 30
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, beijingLoc)
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

func (s *Scheduler) dailyDue(now time.Time) bool {
	if s.lastDay == now.Format("2006-01-02") {
		return false // 今天已执行过
	}
	var hh, mm int
	if _, err := fmt.Sscanf(s.Cfg.CheckinTime, "%02d:%02d", &hh, &mm); err != nil {
		hh, mm = 8, 30
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, beijingLoc)
	return !today.After(now) // 已过今天的签到时刻即补签
}

func (s *Scheduler) setBackoff(id string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.backoff[id]; ok && time.Now().Before(prev) {
		d = prev.Sub(time.Now()) * 2 // 指数退避
	}
	if d > s.Cfg.BackoffMax {
		d = s.Cfg.BackoffMax
	}
	s.backoff[id] = time.Now().Add(d)
}

func (s *Scheduler) retryBackoff(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var due []string
	for id, until := range s.backoff {
		if now.After(until) {
			due = append(due, id)
		}
	}
	s.mu.Unlock()
	for _, id := range due {
		var target *account.Account
		for _, a := range s.Pool.List() {
			if a.ID == id && a.Enabled {
				aa := a
				target = &aa
				break
			}
		}
		if target == nil {
			s.mu.Lock()
			delete(s.backoff, id)
			s.mu.Unlock()
			continue
		}
		res := s.CheckinOne(ctx, *target)
		s.mu.Lock()
		if res.OK {
			delete(s.backoff, id)
		}
		s.mu.Unlock()
	}
}

var beijingLoc = time.FixedZone("Asia/Shanghai", 8*3600)

func beijingNow() time.Time { return time.Now().In(beijingLoc) }

func jsonFloat(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return -1
}

func jsonAny(m map[string]any, key string) any { return m[key] }
