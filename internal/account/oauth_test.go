package account

import (
	"net/url"
	"testing"
)

// TestParseCallbackURL 手动粘贴的完整回调链接应能解析出凭证。
func TestParseCallbackURL(t *testing.T) {
	userJwt := `{"Token":"tok-abc","RefreshToken":"rt-xyz"}`
	userInfo := `{"UserID":"u77","ScreenName":"粘贴号","Region":"CN"}`
	raw := "http://127.0.0.1:18080/authorize?userJwt=" + url.QueryEscape(userJwt) +
		"&userInfo=" + url.QueryEscape(userInfo) + "&login_trace_id=t1"
	parsed, err := ParseCallbackURL(raw)
	if err != nil {
		t.Fatalf("ParseCallbackURL: %v", err)
	}
	if parsed["token"] != "tok-abc" || parsed["refreshToken"] != "rt-xyz" ||
		parsed["userId"] != "u77" || parsed["screenName"] != "粘贴号" {
		t.Fatalf("parsed = %v", parsed)
	}
	// 老流程：只有 refreshToken
	raw2 := "http://127.0.0.1:18080/authorize?refreshToken=rt-only"
	parsed2, err := ParseCallbackURL(raw2)
	if err != nil || parsed2["refreshToken"] != "rt-only" {
		t.Fatalf("legacy flow parsed = %v err = %v", parsed2, err)
	}
	// 无参数链接应报错
	if _, err := ParseCallbackURL("http://127.0.0.1:18080/authorize"); err == nil {
		t.Fatal("empty callback must error")
	}
}
