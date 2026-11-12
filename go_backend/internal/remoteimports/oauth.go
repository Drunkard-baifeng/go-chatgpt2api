package remoteimports

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"github.com/Drunkard-baifeng/public_golibs/httpclienttls"
)

var ErrOAuth = errors.New("OAuth 会话或 callback 无效，请重新授权并粘贴完整回调 URL")
var ErrOAuthBusy = errors.New("OAuth 会话容量已满，请稍后重试")

const oauthClientID = "app_2SKx67EdpoN0G6j64rFvigXD"
const oauthRedirect = "https://platform.openai.com/auth/callback"
const oauthAuth0Client = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"

type oauthSession struct {
	state, verifier string
	created         time.Time
}
type oauthFlow struct {
	mu         sync.Mutex
	sessions   map[string]oauthSession
	processing int
	closing    bool
	exchange   func(context.Context, string, string) (map[string]any, error)
}

func newOAuthFlow(options Options) *oauthFlow {
	f := &oauthFlow{sessions: map[string]oauthSession{}}
	f.exchange = func(ctx context.Context, code, verifier string) (map[string]any, error) {
		return exchangeOAuth(ctx, options, code, verifier)
	}
	return f
}
func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, e := rand.Read(data); e != nil {
		return "", ErrStorage
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func (f *oauthFlow) purgeLocked() {
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, session := range f.sessions {
		if session.created.Before(cutoff) {
			delete(f.sessions, id)
		}
	}
}
func (f *oauthFlow) close() { f.mu.Lock(); defer f.mu.Unlock(); f.closing = true; clear(f.sessions) }
func (f *oauthFlow) start(email string) (map[string]string, error) {
	if len(email) > 512 || strings.ContainsAny(email, "\r\n\x00") {
		return nil, ErrInput
	}
	verifier, e := randomURLToken(32)
	if e != nil {
		return nil, e
	}
	nonce, e := randomURLToken(32)
	if e != nil {
		return nil, e
	}
	sid, e := newID()
	if e != nil {
		return nil, e
	}
	suffix, e := randomURLToken(16)
	if e != nil {
		return nil, e
	}
	device, e := newID()
	if e != nil {
		return nil, e
	}
	state := sid + "." + suffix
	hash := sha256.Sum256([]byte(verifier))
	query := url.Values{"issuer": {"https://auth.openai.com"}, "client_id": {oauthClientID}, "audience": {"https://api.openai.com/v1"}, "redirect_uri": {oauthRedirect}, "device_id": {device}, "screen_hint": {"login_or_signup"}, "max_age": {"0"}, "scope": {"openid profile email offline_access"}, "response_type": {"code"}, "response_mode": {"query"}, "state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "auth0Client": {oauthAuth0Client}}
	if email = strings.TrimSpace(email); email != "" {
		query.Set("login_hint", email)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgeLocked()
	if f.closing {
		return nil, context.Canceled
	}
	if len(f.sessions) >= 64 {
		return nil, ErrOAuthBusy
	}
	f.sessions[sid] = oauthSession{state: state, verifier: verifier, created: time.Now()}
	return map[string]string{"session_id": sid, "authorize_url": "https://auth.openai.com/api/accounts/authorize?" + query.Encode(), "expires_in": "600", "redirect_uri_prefix": oauthRedirect}, nil
}
func (f *oauthFlow) finish(ctx context.Context, sid, callback string) (map[string]any, error) {
	if len(callback) > 16384 || len(sid) > 128 || strings.ContainsAny(callback, "\r\n\x00") {
		return nil, ErrOAuth
	}
	u, e := url.Parse(strings.TrimSpace(callback))
	if e != nil || u.Scheme != "https" || u.Host != "platform.openai.com" || u.Path != "/auth/callback" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrOAuth
	}
	query, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(query["code"]) != 1 || len(query["state"]) != 1 || query.Get("error") != "" {
		return nil, ErrOAuth
	}
	code, state := query.Get("code"), query.Get("state")
	if code == "" || len(code) > 8192 || strings.ContainsAny(code, "\r\n\x00") || len(state) > 256 {
		return nil, ErrOAuth
	}
	stateSID, _, ok := strings.Cut(state, ".")
	if !ok || (sid != "" && sid != stateSID) {
		return nil, ErrOAuth
	}
	sid = stateSID
	f.mu.Lock()
	f.purgeLocked()
	session, exists := f.sessions[sid]
	if f.closing {
		f.mu.Unlock()
		return nil, context.Canceled
	}
	if !exists || subtle.ConstantTimeCompare([]byte(state), []byte(session.state)) != 1 {
		f.mu.Unlock()
		return nil, ErrOAuth
	}
	if f.processing >= 8 {
		f.mu.Unlock()
		return nil, ErrOAuthBusy
	}
	// 在网络请求前单次消费，失败也不能并发或重复兑换同一授权码。
	delete(f.sessions, sid)
	f.processing++
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.processing--; f.mu.Unlock() }()
	return f.exchange(ctx, code, session.verifier)
}
func exchangeOAuth(parent context.Context, options Options, code, verifier string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	proxy := ""
	if options.ResolveProxy != nil {
		var e error
		proxy, e = options.ResolveProxy(ctx)
		if e != nil {
			return nil, ErrRemote
		}
	}
	if options.Upstream.Profile == "" {
		return nil, ErrRemote
	}
	client, e := httpclienttls.NewClient(httpclienttls.Config{Profile: options.Upstream.Profile, Proxy: proxy, Verify: httpclienttls.BoolPtr(true), StrictFingerprint: true, EnableHTTP3: false, EnableProtocolRacing: false, MaxResponseBodyBytes: 1 << 20, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, DisableKeepAlives: true})
	if e != nil {
		return nil, ErrRemote
	}
	defer client.Close()
	body, _ := json.Marshal(map[string]string{"client_id": oauthClientID, "code_verifier": verifier, "grant_type": "authorization_code", "code": code, "redirect_uri": oauthRedirect})
	redirect := false
	response, e := client.DoStream(ctx, "POST", "https://auth.openai.com/api/accounts/oauth/token", bytes.NewReader(body), &httpclienttls.Options{HeaderValues: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Referer": {"https://platform.openai.com/"}, "Origin": {"https://platform.openai.com"}, "Auth0-Client": {oauthAuth0Client}}, BrowserMode: httpclienttls.BrowserModeFetch, AllowRedirects: &redirect, DisableTimeout: true, HeaderTimeout: 20 * time.Second, ReadIdleTimeout: 20 * time.Second})
	if e != nil {
		return nil, safeNetwork(ctx, e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, ErrRemote
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil {
		return nil, safeNetwork(ctx, e)
	}
	if len(raw) > 1<<20 {
		return nil, ErrRemote
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return nil, ErrRemote
	}
	out := map[string]any{"source_type": "web"}
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		v, ok := payload[key].(string)
		if !ok && key != "id_token" {
			return nil, ErrRemote
		}
		if len(v) > 65536 || strings.ContainsAny(v, "\r\n\x00") || ((key == "access_token" || key == "refresh_token") && strings.TrimSpace(v) == "") {
			return nil, ErrRemote
		}
		out[key] = strings.TrimSpace(v)
	}
	return out, nil
}
func (s *Service) OAuthStart(ctx context.Context, email string) (map[string]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.ctx.Err() != nil {
		return nil, context.Canceled
	}
	return s.oauth.start(email)
}
func (s *Service) OAuthFinish(parent context.Context, sid, callback string, target *string) (accounts.Operation, error) {
	if e := s.target(parent, target); e != nil {
		return accounts.Operation{}, e
	}
	ctx, cancel := context.WithTimeout(parent, 70*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if s.ctx.Err() != nil {
		return accounts.Operation{}, context.Canceled
	}
	var payload map[string]any
	e := s.accounts.WithProcessing(ctx, func(ctx context.Context) error {
		var err error
		payload, err = s.oauth.finish(ctx, sid, callback)
		return err
	})
	if e != nil {
		return accounts.Operation{}, e
	}
	var result accounts.Operation
	e = retryBusy(ctx, func() error {
		var err error
		noItems := false
		result, err = s.accounts.Import(ctx, accounts.ImportRequest{Accounts: []map[string]any{payload}, TargetGroup: target, ReturnItems: &noItems})
		return err
	})
	return result, e
}
