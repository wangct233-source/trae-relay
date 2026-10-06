package account

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"trae-relay/internal/config"
)

func base64URL(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func isDigit(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func newTestPool(t *testing.T, dir string) *Pool {
	t.Helper()
	cfg := &config.Config{
		DataDir: dir, SlotPerAccount: 1, SlotQueueTimeout: 1000,
		CooldownOnErr: 3, CooldownSeconds: 300,
	}
	client := NewUpstreamClient("http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1", "/x")
	p, err := NewPool(cfg, client)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

var _ = fmt.Sprint

func TestJWTUserAndExpiry(t *testing.T) {
	// 构造假 JWT：header.payload.sig（payload 含 data.id 与 exp）
	payload := `{"data":{"id":"u123"},"exp":1750000000}`
	b64 := base64URL(payload)
	token := "abc." + b64 + ".sig"
	if got := JWTUser(token); got != "u123" {
		t.Fatalf("JWTUser = %q, want u123", got)
	}
	if got := JWTExpiry(token); got != 1750000000 {
		t.Fatalf("JWTExpiry = %d, want 1750000000", got)
	}
	if got := JWTUser("not-a-jwt"); got != "" {
		t.Fatalf("JWTUser(bad) = %q, want empty", got)
	}
}

func TestCheckinDeviceID(t *testing.T) {
	a := CheckinDeviceID("userA", 0)
	b := CheckinDeviceID("userA", 1)
	c := CheckinDeviceID("userB", 0)
	if a != CheckinDeviceID("userA", 0) {
		t.Fatal("device id must be deterministic")
	}
	if a == b {
		t.Fatal("generation must change device id (9074 rotation)")
	}
	if a == c {
		t.Fatal("different users must get different device ids")
	}
	if len(a) != 16 || !isDigit(a) {
		t.Fatalf("device id %q must be 16 digits", a)
	}
}

func TestPoolImportAndSchedule(t *testing.T) {
	dir := t.TempDir()
	p := newTestPool(t, dir)
	added, updated, err := p.ImportJSON(`[
		{"token":"tok1","user_id":"u1","label":"主号"},
		{"token":"tok2","refresh_token":"r2"},
		"not-an-object"
	]`)
	// 非对象项被跳过，不报错
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if added != 2 || updated != 0 {
		t.Fatalf("added=%d updated=%d, want 2/0", added, updated)
	}
	// 同 user_id 覆盖更新
	added, updated, err = p.ImportJSON(`{"token":"tok1-new","user_id":"u1","label":"改名"}`)
	if err != nil || added != 0 || updated != 1 {
		t.Fatalf("reimport: added=%d updated=%d err=%v", added, updated, err)
	}
	list := p.List()
	if len(list) != 2 {
		t.Fatalf("list=%d, want 2", len(list))
	}
	for _, a := range list {
		if a.ID == "u1" && a.Label != "改名" {
			t.Fatalf("label merge failed: %q", a.Label)
		}
		if a.ID == "u1" && a.Token != "tok1-new" {
			t.Fatalf("token update failed: %q", a.Token)
		}
	}
	// 调度：默认轮询
	ids := map[string]int{}
	for i := 0; i < 4; i++ {
		l, err := p.Acquire(context.Background(), "round-robin")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		ids[l.Account.ID]++
		l.Release()
	}
	if len(ids) != 2 {
		t.Fatalf("round-robin should hit 2 accounts, got %v", ids)
	}
	// 禁用后只剩一个可调度
	_ = p.SetEnabled("u1", false)
	l, err := p.Acquire(context.Background(), "round-robin")
	if err != nil {
		t.Fatalf("Acquire after disable: %v", err)
	}
	if l.Account.ID != list[1].ID && l.Account.ID != "u1" {
		// 只剩非 u1 的那个
	}
	if l.Account.ID == "u1" {
		t.Fatal("disabled account must not be scheduled")
	}
	l.Release()
}

func TestPoolCooldown(t *testing.T) {
	dir := t.TempDir()
	p := newTestPool(t, dir)
	_, _, _ = p.ImportJSON(`{"token":"tok","user_id":"u1"}`)
	for i := 0; i < 3; i++ { // CooldownOnErr=3（测试池配置）
		p.ReportError("u1", "boom")
	}
	_, err := p.Acquire(context.Background(), "round-robin")
	if err == nil || !strings.Contains(err.Error(), "冷却") {
		t.Fatalf("expect cooldown error, got %v", err)
	}
	p.ReportSuccess("u1")
	// 冷却未解除仍不可调度
	if _, err := p.Acquire(context.Background(), "round-robin"); err == nil {
		t.Fatal("cooldown must block scheduling")
	}
}
