// Package account —— OAuth 回调参数解析（trae2api-cn parse_oauth_params 的 Go 版，
// auth-helper 与 relay /api/web-auth 共用同一实现）。
package account

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

var errInvalidCallback = errors.New("回调链接中没有可解析的参数")

// ParseOAuthQuery 解析 Trae 授权回调 query。
// 新流程回调带 userJwt JSON（含 Token/RefreshToken）；老流程只带 refreshToken，
// 由调用方向 oauth/ExchangeToken 兑换 Cloud-IDE-JWT。
func ParseOAuthQuery(q url.Values) map[string]string {
	jload := func(key string) map[string]any {
		var m map[string]any
		if v := q.Get(key); v != "" {
			_ = json.Unmarshal([]byte(v), &m)
		}
		return m
	}
	get := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	first := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}
	userJwt := jload("userJwt")
	userInfo := jload("userInfo")
	token := get(userJwt, "Token", "token")
	refresh := get(userJwt, "RefreshToken", "refreshToken")
	if refresh == "" {
		refresh = first(q.Get("refreshToken"), q.Get("data"))
	}
	userID := get(userInfo, "UserID", "userId", "userID")
	if userID == "" {
		userID = q.Get("userId")
	}
	region := get(userInfo, "Region", "region")
	if region == "" {
		region = first(q.Get("region"), "CN")
	}
	clientID := get(userJwt, "ClientID", "clientId")
	if clientID == "" {
		clientID = first(q.Get("clientID"), q.Get("clientId"), q.Get("client_id"))
	}
	host := first(q.Get("host"), get(userInfo, "Host", "host"))
	return map[string]string{
		"token":           token,
		"refreshToken":    refresh,
		"userId":          userID,
		"tenantId":        get(userInfo, "TenantID", "tenantId"),
		"region":          region,
		"host":            host,
		"clientId":        clientID,
		"webId":           get(userInfo, "WebId", "webId"),
		"bizUserId":       get(userInfo, "BizUserId", "bizUserId"),
		"userUniqueId":    get(userInfo, "UserUniqueId", "userUniqueId"),
		"scope":           first(q.Get("scope"), get(userInfo, "Scope", "scope")),
		"tenant":          get(userInfo, "Tenant", "tenant"),
		"userRegion":      first(q.Get("userRegion"), get(userInfo, "UserRegion", "userRegion")),
		"userIdentity":    get(userInfo, "UserIdentity", "userIdentity"),
		"screenName":      get(userInfo, "ScreenName", "screenName"),
		"expiredAt":       get(userJwt, "TokenExpireAt", "tokenExpireAt"),
		"refreshExpireAt": get(userJwt, "RefreshExpireAt", "refreshExpireAt"),
	}
}

// ParseCallbackURL 从完整回调链接中解析凭证（手动粘贴模式）。
// 链接形如 http://127.0.0.1:18080/authorize?userJwt=...&userInfo=...
func ParseCallbackURL(raw string) (map[string]string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if len(u.Query()) == 0 {
		return nil, errInvalidCallback
	}
	return ParseOAuthQuery(u.Query()), nil
}
