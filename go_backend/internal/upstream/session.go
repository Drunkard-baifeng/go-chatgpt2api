// Package upstream 为单一账号和代理组合拥有 Cookie、连接及请求取消生命周期。
package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/config"
	"github.com/Drunkard-baifeng/public_golibs/httpclienttls"
)

const (
	acceptLanguage = "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7"
	oaiLanguage    = "zh-CN"
)

var (
	ErrTarget           = errors.New("上游只接受固定站点内的相对路径")
	ErrTransport        = errors.New("上游传输失败")
	ErrResponseTooLarge = errors.New("上游响应超过字节限制")
)

type Session struct {
	proxy    string
	transfer func(context.Context, string, string, string, []byte, string) ([]byte, error)
	client   *httpclienttls.Client
	policy   config.Upstream
	origin   string
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// NewSession 的调用者为每个账号和代理组合分别创建并关闭实例。
// 目标固定，不接收外部 URL；OAuth 和下载需要另行实现各自目标策略。
func NewSession(policy config.Upstream, proxy string) (*Session, error) {
	return newSession(policy, proxy, "https://chatgpt.com")
}

func newSession(policy config.Upstream, proxy, origin string) (*Session, error) {
	if policy.Profile == "" || policy.TotalTimeoutSeconds < 1 || policy.HeaderTimeoutSeconds < 1 || policy.ReadIdleTimeoutSeconds < 1 || policy.MaxResponseBytes < 1 {
		return nil, errors.New("上游策略无效")
	}
	client, err := httpclienttls.NewClient(httpclienttls.Config{
		Profile: policy.Profile, Proxy: proxy, StrictFingerprint: true,
		AcceptLanguage: acceptLanguage,
		EnableHTTP3:    false, EnableProtocolRacing: false,
		MaxResponseBodyBytes: policy.MaxResponseBytes,
		MaxIdleConns:         4, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 4,
	})
	if err != nil {
		return nil, errors.New("TLS 客户端配置无效，请检查 profile 和代理")
	}
	ctx, cancel := context.WithCancel(context.Background())
	transfer := func(ctx context.Context, method, raw, proxy string, data []byte, media string) ([]byte, error) {
		body, err := assettransfer.Transfer(ctx, method, raw, proxy, data, media, http.Header{
			"Origin": {origin}, "Referer": {origin + "/"}, "User-Agent": {client.Profile().UserAgent},
		})
		var failure *assettransfer.Failure
		if errors.As(err, &failure) && failure.Status != 0 {
			stage := "image_download"
			if method == "PUT" {
				stage = "upload"
			}
			callrecord.RecordEvent(ctx, callrecord.DiagnosticEvent{Event: "http_headers_received", Stage: stage, Status: "failed", StatusCode: failure.Status})
		}
		return body, err
	}
	return &Session{client: client, policy: policy, origin: origin, ctx: ctx, cancel: cancel, proxy: proxy, transfer: transfer}, nil
}

// Do 不自动重放或跟随重定向。调用方必须关闭 Body，包括拒绝状态码时。
func (s *Session) Do(parent context.Context, method, path string, body io.Reader, headers http.Header) (*Response, error) {
	return s.do(parent, method, path, body, headers, httpclienttls.BrowserModeFetch)
}

func (s *Session) do(parent context.Context, method, path string, body io.Reader, headers http.Header, mode httpclienttls.BrowserMode) (*Response, error) {
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return nil, ErrTarget
	}
	for key := range headers {
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "Proxy-Authorization") {
			return nil, ErrTarget
		}
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(s.policy.TotalTimeoutSeconds)*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	if s.ctx.Err() != nil {
		cleanup()
		return nil, context.Canceled
	}
	ctx, diagnostics := beginHTTPDiagnostics(ctx)
	redirect := false
	response, err := s.client.DoStream(ctx, method, s.origin+path, body, &httpclienttls.Options{
		HeaderValues: headers.Clone(), BrowserMode: mode,
		DisableTimeout: true, AllowRedirects: &redirect,
		HeaderTimeout:   time.Duration(s.policy.HeaderTimeoutSeconds) * time.Second,
		ReadIdleTimeout: time.Duration(s.policy.ReadIdleTimeoutSeconds) * time.Second,
	})
	if err != nil {
		safe := safeError(ctx, err)
		diagnostics.finish(safe)
		cleanup()
		return nil, safe
	}
	diagnostics.headers(response.StatusCode, response.Headers.Get("X-Request-ID"))
	limited := &responseBody{raw: response.Body, remaining: s.policy.MaxResponseBytes, cleanup: cleanup, ctx: ctx, diagnostics: diagnostics}
	return &Response{StatusCode: response.StatusCode, Header: response.Headers.Clone(), Body: limited}, nil
}

func (s *Session) Close() {
	s.once.Do(func() { s.cancel(); s.client.Close() })
}

func safeError(ctx context.Context, err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return ErrTransport
}

type responseBody struct {
	raw         io.ReadCloser
	remaining   int64
	cleanup     func()
	ctx         context.Context
	once        sync.Once
	closeErr    error
	diagnostics *httpDiagnostics
}

func (b *responseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.raw.Read(probe[:])
		if n > 0 {
			if b.diagnostics != nil {
				b.diagnostics.finish(ErrResponseTooLarge)
			}
			b.Close()
			return 0, ErrResponseTooLarge
		}
		safe := safeError(b.ctx, err)
		if err != nil {
			if b.diagnostics != nil {
				b.diagnostics.finish(safe)
			}
			b.Close()
		}
		return 0, safe
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.raw.Read(p)
	b.remaining -= int64(n)
	safe := safeError(b.ctx, err)
	if err != nil {
		if b.diagnostics != nil {
			b.diagnostics.finish(safe)
		}
		b.Close()
	}
	return n, safe
}

func (b *responseBody) Close() error {
	b.once.Do(func() {
		if b.diagnostics != nil {
			b.diagnostics.finish(b.ctx.Err())
		}
		b.cleanup()
		b.closeErr = b.raw.Close()
	})
	return b.closeErr
}
