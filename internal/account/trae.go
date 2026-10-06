package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UpstreamClient 封装 Trae 上游管理类 API：
// ExchangeToken、GetUserInfo、签到 claim/status、积分权益查询。
// 协议细节对照 trae-反代/src/trae_client.py 与 main.py 提取。
type UpstreamClient struct {
	AuthHost  string
	UGHost    string
	PayHost   string
	LoginPath string
	HTTP      *http.Client
}

func NewUpstreamClient(authHost, ugHost, payHost, loginPath string) *UpstreamClient {
	return &UpstreamClient{
		AuthHost:  strings.TrimRight(authHost, "/"),
		UGHost:    strings.TrimRight(ugHost, "/"),
		PayHost:   strings.TrimRight(payHost, "/"),
		LoginPath: loginPath,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// postJSON 通用 POST，返回解析后的 JSON 对象。
func (c *UpstreamClient) postJSON(ctx context.Context, url string, payload any, headers map[string]string) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream [%d]: %.300s", resp.StatusCode, raw)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("invalid json: %.200s", raw)
	}
	return data, nil
}

func authHeaders(token, deviceID string) map[string]string {
	h := map[string]string{
		"Authorization": "Cloud-IDE-JWT " + token,
	}
	if deviceID != "" {
		h["x-device-id"] = deviceID
		h["x-device-brand"] = "ASUS TUF Gaming A15 FA507RM_FA507RM"
		h["x-device-type"] = "windows"
	}
	return h
}

// ExchangeToken 用 RefreshToken 兑换 Cloud-IDE-JWT。
// POST {auth}/cloudide/api/v3/trae/oauth/ExchangeToken
func (c *UpstreamClient) ExchangeToken(ctx context.Context, refreshToken, clientID string) (token string, expiry int64, userID string, err error) {
	data, err := c.postJSON(ctx, c.AuthHost+"/cloudide/api/v3/trae/oauth/ExchangeToken", map[string]any{
		"ClientID": clientID, "RefreshToken": refreshToken,
		"ClientSecret": "-", "UserID": "",
	}, nil)
	if err != nil {
		return "", 0, "", err
	}
	result, _ := data["Result"].(map[string]any)
	if result == nil {
		result, _ = data["result"].(map[string]any)
	}
	if result == nil {
		result = data
	}
	for _, k := range []string{"Token", "token", "AccessToken", "accessToken"} {
		if s, _ := result[k].(string); s != "" {
			token = s
			break
		}
	}
	if token == "" {
		return "", 0, "", fmt.Errorf("ExchangeToken: missing token in response")
	}
	expiry = JWTExpiry(token)
	if v, ok := result["ExpiredAt"]; ok {
		expiry = toUnix(v)
	}
	userID, _ = result["UserID"].(string)
	if userID == "" {
		userID = JWTUser(token)
	}
	return token, expiry, userID, nil
}

// GetUserInfo 拉取上游昵称与 UserID。
// POST {auth}/cloudide/api/v3/trae/GetUserInfo，JWT 走 x-cloudide-token 头。
func (c *UpstreamClient) GetUserInfo(ctx context.Context, token string) (name, userID string, err error) {
	data, err := c.postJSON(ctx, c.AuthHost+"/cloudide/api/v3/trae/GetUserInfo", nil,
		map[string]string{"x-cloudide-token": token})
	if err != nil {
		return "", "", err
	}
	result, _ := data["Result"].(map[string]any)
	if result == nil {
		result = data
	}
	name, _ = result["ScreenName"].(string)
	userID, _ = result["UserID"].(string)
	return name, userID, nil
}

// CheckinStatus 查询签到状态：enable / checked_in / 今日可得积分。
// POST {ug}/trae/api/v2/ug/checkin_credits/status
func (c *UpstreamClient) CheckinStatus(ctx context.Context, token, deviceID string) (map[string]any, error) {
	return c.postJSON(ctx, c.UGHost+"/trae/api/v2/ug/checkin_credits/status", nil, authHeaders(token, deviceID))
}

// CheckinClaim 领取今日签到积分。业务码 9074（设备限流）原样返回给调用方处理。
// POST {ug}/trae/api/v2/ug/checkin_credits/claim
func (c *UpstreamClient) CheckinClaim(ctx context.Context, token, deviceID string) (map[string]any, error) {
	return c.postJSON(ctx, c.UGHost+"/trae/api/v2/ug/checkin_credits/claim", nil, authHeaders(token, deviceID))
}

// FetchCredits 查询账号积分权益包（通用积分汇总）。
// POST {pay}/trae/api/v2/pay/ide_user_ent_usage {require_usage:true, req_source:0}
// 返回 remaining、total、unlimited；无法解析时 ok=false。
func (c *UpstreamClient) FetchCredits(ctx context.Context, token, deviceID string) (remaining, total float64, unlimited, ok bool, err error) {
	data, err := c.postJSON(ctx, c.PayHost+"/trae/api/v2/pay/ide_user_ent_usage",
		map[string]any{"require_usage": true, "req_source": 0}, authHeaders(token, deviceID))
	if err != nil {
		return 0, 0, false, false, err
	}
	if code, has := data["code"]; has && toFloat(code) != 0 {
		return 0, 0, false, false, fmt.Errorf("credits: %v", data["message"])
	}
	packs, _ := data["user_entitlement_pack_list"].([]any)
	haveLimit := false
	for _, p := range packs {
		pack, _ := p.(map[string]any)
		if pack == nil {
			continue
		}
		bi, _ := pack["entitlement_base_info"].(map[string]any)
		usage, _ := pack["usage"].(map[string]any)
		quota, _ := bi["quota"].(map[string]any)
		limit := toFloat(quota["credits_limit"])
		if !hasValue(quota, "credits_limit") {
			continue // product_type=0 纯功能包无积分
		}
		haveLimit = true
		amount := toFloat(usage["credits_amount"])
		if limit == -1 {
			unlimited = true
		} else {
			total += limit
			remaining += limit - amount
		}
	}
	if !haveLimit {
		return 0, 0, false, false, nil
	}
	if unlimited {
		return 0, 0, true, true, nil
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining, total, false, true, nil
}

// PasswordLogin 账号密码登录导入（实验性）。
// 上游登录端点未公开文档化，默认路径可用 TRAE_LOGIN_PATH 环境变量覆盖。
// 请求体透传 phone/password/captcha 等字段，响应按 Result.RefreshToken 提取。
func (c *UpstreamClient) PasswordLogin(ctx context.Context, phone, password, captcha string) (refreshToken, userID string, err error) {
	payload := map[string]any{"Phone": phone, "Password": password, "ClientSecret": "-"}
	if captcha != "" {
		payload["Captcha"] = captcha
	}
	data, err := c.postJSON(ctx, c.AuthHost+c.LoginPath, payload, nil)
	if err != nil {
		return "", "", fmt.Errorf("password login: %w", err)
	}
	result, _ := data["Result"].(map[string]any)
	if result == nil {
		result, _ = data["result"].(map[string]any)
	}
	if result == nil {
		result = data
	}
	refreshToken, _ = firstString(result, "RefreshToken", "refresh_token")
	userID, _ = firstString(result, "UserID", "user_id", "userId")
	if refreshToken == "" {
		msg, _ := firstString(data, "message", "Message")
		return "", "", fmt.Errorf("password login failed: %s", msg)
	}
	return refreshToken, userID, nil
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

func hasValue(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	return !math.IsNaN(toFloat(v))
}

// toUnix 宽松解析时间值（unix 秒/毫秒/ISO 字符串）为 unix 秒。
func toUnix(v any) int64 {
	switch t := v.(type) {
	case float64:
		if t > 1e12 {
			t /= 1000
		}
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			if n > 1e12 {
				n /= 1000
			}
			return n
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts.Unix()
		}
	}
	return 0
}
