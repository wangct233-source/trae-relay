package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"trae-relay/internal/account"
	"trae-relay/internal/config"
)

// TestWebAuthImport 新流程：回调直接带 token，应入库为启用账号。
func TestWebAuthImport(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, SlotPerAccount: 1, CooldownOnErr: 3}
	pool, err := account.NewPool(cfg, account.NewUpstreamClient("http://x", "http://x", "http://x", "/x"))
	if err != nil {
		t.Fatal(err)
	}
	d := &WebAuthDeps{Pool: pool, Upstream: account.NewUpstreamClient("http://x", "http://x", "http://x", "/x")}

	// 构造含 data.id 的假 JWT
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"data":{"id":"u9"},"exp":1893456000}`)) + ".s"
	body, _ := json.Marshal(map[string]any{
		"token": jwt, "userId": "u9", "screenName": "网页号", "region": "CN",
	})
	req := httptest.NewRequest("POST", "/api/web-auth", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	d.HandleWebAuth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Success {
		t.Fatalf("success=false: %s", rec.Body.String())
	}
	list := pool.List()
	if len(list) != 1 || list[0].ID != "u9" || !list[0].Enabled || list[0].Label != "网页号" {
		t.Fatalf("pool = %+v", list)
	}
	// 同 ID 二次授权应更新而非新增
	body2, _ := json.Marshal(map[string]any{"token": jwt, "userId": "u9", "screenName": "改名"})
	req2 := httptest.NewRequest("POST", "/api/web-auth", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	d.HandleWebAuth(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("re-auth status=%d", rec2.Code)
	}
	if n := len(pool.List()); n != 1 {
		t.Fatalf("re-auth created duplicate: %d accounts", n)
	}
}

// TestWebAuthMissingCreds 缺凭证应 400。
func TestWebAuthMissingCreds(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}
	pool, _ := account.NewPool(cfg, account.NewUpstreamClient("http://x", "http://x", "http://x", "/x"))
	d := &WebAuthDeps{Pool: pool, Upstream: account.NewUpstreamClient("http://x", "http://x", "http://x", "/x")}
	req := httptest.NewRequest("POST", "/api/web-auth", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	d.HandleWebAuth(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}
