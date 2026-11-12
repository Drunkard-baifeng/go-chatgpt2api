package prompts

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
)

var ErrRegistry = errors.New("提示词注册表校验或下载失败")

// fetchURL never carries account identity, cookies or an environment proxy.
// Every redirect is re-resolved and checked; the actual socket receives only
// the selected public IP while HTTP Host and TLS SNI retain the original host.
func fetchURL(parent context.Context, raw string, limit int64) ([]byte, error) {
	return fetchWith(parent, raw, limit, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
}

func fetchWith(parent context.Context, raw string, limit int64, lookup func(context.Context, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for hop := 0; hop <= 3; hop++ {
		u, err := validURL(raw, false)
		if err != nil {
			return nil, ErrRegistry
		}
		addresses, err := lookup(ctx, u.Hostname())
		if err != nil || len(addresses) == 0 {
			return nil, fetchError(ctx)
		}
		for _, address := range addresses {
			if !assettransfer.PublicAddress(address) {
				return nil, ErrRegistry
			}
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		originalHost, serverName := u.Host, u.Hostname()
		u.Host = net.JoinHostPort(addresses[0].Unmap().String(), port)
		transport := &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true, DisableCompression: true}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			transport.CloseIdleConnections()
			return nil, ErrRegistry
		}
		req.Host = originalHost
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "chatgpt2api-go-prompt-library/1.0")
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(req)
		if err != nil {
			transport.CloseIdleConnections()
			return nil, fetchError(ctx)
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			transport.CloseIdleConnections()
			base, _ := url.Parse(raw)
			next, err := base.Parse(location)
			if err != nil || location == "" || hop == 3 || (base.Scheme == "https" && next.Scheme != "https") {
				return nil, ErrRegistry
			}
			raw = next.String()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || response.ContentLength > limit || strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") || response.Header.Get("Content-Encoding") != "" {
			response.Body.Close()
			transport.CloseIdleConnections()
			return nil, ErrRegistry
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		response.Body.Close()
		transport.CloseIdleConnections()
		if err != nil || int64(len(body)) > limit {
			return nil, fetchError(ctx)
		}
		return body, nil
	}
	return nil, ErrRegistry
}
func fetchError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRegistry
}
