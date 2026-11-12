package upstream

import (
	"chatgpt2api/go_backend/internal/config"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AccountInfo struct {
	Email, UserID, Plan, DefaultModel string
	Quota                             int
	Unknown                           bool
	RestoreAt                         any
	Limits                            []any
}

// AccountInfo 只查询账号信息，不进行 Sentinel 挑战或生成提交。
func (s *Session) AccountInfo(parent context.Context, token string) (AccountInfo, error) {
	var info AccountInfo
	if !safeHeaderToken(token) {
		return info, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}, "Content-Type": {"application/json"}, "Origin": {s.origin}, "Referer": {s.origin + "/"}, "Oai-Device-Id": {uuid()}, "Oai-Session-Id": {uuid()}, "Oai-Language": {oaiLanguage}}
	get := func(path string) (map[string]any, error) {
		res, e := s.Do(ctx, "GET", path, nil, headers)
		if e != nil {
			return nil, e
		}
		data, e := readResponse(res, "account_sync", 1<<20)
		if e != nil {
			return nil, e
		}
		var out map[string]any
		if json.Unmarshal(data, &out) != nil || out == nil {
			return nil, ErrProtocol
		}
		return out, nil
	}
	me, err := get("/backend-api/me")
	if err != nil {
		return info, err
	}
	var init map[string]any
	err = s.postJSON(ctx, "/backend-api/conversation/init", headers, map[string]any{"gizmo_id": nil, "requested_default_model": nil, "conversation_id": nil, "timezone_offset_min": -480}, &init, "account_sync")
	if err != nil {
		return info, err
	}
	if init == nil {
		return info, ErrProtocol
	}
	account, err := get("/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480")
	if err != nil {
		return info, err
	}
	info.Email, _ = me["email"].(string)
	info.UserID, _ = me["id"].(string)
	if info.Email == "" && info.UserID == "" {
		return info, ErrProtocol
	}
	if list, ok := account["accounts"].(map[string]any); ok {
		if def, ok := list["default"].(map[string]any); ok {
			if a, ok := def["account"].(map[string]any); ok {
				info.Plan, _ = a["plan_type"].(string)
			}
		}
	}
	info.Limits, _ = init["limits_progress"].([]any)
	info.Unknown = true
	info.DefaultModel, _ = init["default_model_slug"].(string)
	for _, raw := range info.Limits {
		if m, ok := raw.(map[string]any); ok && m["feature_name"] == "image_gen" {
			n, ok := m["remaining"].(float64)
			if !ok || n < 0 || n > 100000000 || n != float64(int(n)) {
				return info, ErrProtocol
			}
			info.Quota = int(n)
			info.Unknown = false
			info.RestoreAt = m["reset_after"]
			break
		}
	}
	return info, nil
}

type OAuthTokens struct{ AccessToken, RefreshToken string }

var ErrOAuthTerminal = errors.New("RT 已被上游确认失效")

// RefreshOAuth 使用隔离的 auth.openai.com 会话，不携带 AT 或聊天 Cookie。
func RefreshOAuth(ctx context.Context, policy config.Upstream, proxy, rt string) (OAuthTokens, error) {
	policy.TotalTimeoutSeconds = 60
	s, err := newSession(policy, proxy, "https://auth.openai.com")
	if err != nil {
		return OAuthTokens{}, err
	}
	defer s.Close()
	return refreshOAuth(ctx, s, rt)
}
func refreshOAuth(parent context.Context, s *Session, rt string) (OAuthTokens, error) {
	var tokens OAuthTokens
	if !safeHeaderToken(rt) {
		return tokens, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"app_2SKx67EdpoN0G6j64rFvigXD"}}
	response, err := s.Do(ctx, "POST", "/oauth/token", strings.NewReader(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}})
	if err != nil {
		return tokens, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return tokens, err
	}
	if len(raw) > 1<<20 {
		return tokens, ErrResponseTooLarge
	}
	var payload struct {
		AT          string          `json:"access_token"`
		RT          string          `json:"refresh_token"`
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return tokens, ErrProtocol
	}
	if response.StatusCode != 200 {
		code := ""
		_ = json.Unmarshal(payload.Error, &code)
		if code == "" {
			var nested struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(payload.Error, &nested)
			code = nested.Code
		}
		if code == "invalid_grant" || code == "invalid_refresh_token" || code == "refresh_token_invalidated" || strings.Contains(strings.ToLower(payload.Description), "session has ended") {
			return tokens, ErrOAuthTerminal
		}
		return tokens, checkStatus(response.StatusCode, "oauth_refresh")
	}
	if !safeHeaderToken(payload.AT) {
		return tokens, ErrProtocol
	}
	if payload.RT == "" {
		payload.RT = rt
	}
	if !safeHeaderToken(payload.RT) {
		return tokens, ErrProtocol
	}
	return OAuthTokens{AccessToken: payload.AT, RefreshToken: payload.RT}, nil
}
