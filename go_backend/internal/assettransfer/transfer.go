// Package assettransfer 负责不携带账号身份的图片字节传输，固定每跳实际目标 IP。
package assettransfer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const MaxBytes = 50 << 20

var ErrTransfer = errors.New("图片地址无效、目标非公网或数据传输失败")

// Failure 不在 Error 中包含签名 URL、代理信息或响应正文。
// 上游适配器负责把有界正文脱敏后写入诊断，不能把签名资源拒绝当成账号失效。
type Failure struct {
	Method, Phase, Reason, Body string
	Status                      int
	TimedOut                    bool
}

func (e *Failure) Error() string { return ErrTransfer.Error() }
func (e *Failure) Unwrap() error { return ErrTransfer }

func transferFailure(method, phase string, err error) *Failure {
	reason := "连接失败"
	var network net.Error
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	timedOut := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout())
	switch {
	case timedOut:
		reason = "等待超时"
	case errors.As(err, &dns):
		reason = "域名解析失败"
	case errors.As(err, &certificate), errors.As(err, &unknown):
		reason = "TLS 证书验证失败"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		reason = "连接被提前关闭"
	}
	return &Failure{Method: method, Phase: phase, Reason: reason, TimedOut: timedOut}
}

var denied = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}

func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, raw := range denied {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

// Transfer 不使用环境代理，不转发 Authorization/Cookie，不自动重试 PUT。
// 直连仅拨号已验证地址，保留 URL、Host 与 TLS 域名；代理隧道只接收固定 IP。
// 上传允许附加 Origin、Referer、User-Agent；不会接收账号身份请求头。
func Transfer(parent context.Context, method, raw, proxy string, data []byte, media string, uploadHeaders ...http.Header) ([]byte, error) {
	timeout := 60 * time.Second
	if method == "PUT" {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if method != "GET" && method != "PUT" {
		return nil, ErrTransfer
	}
	var proxyURL *url.URL
	if proxy != "" {
		var err error
		proxyURL, err = url.Parse(proxy)
		if err != nil || proxyURL.Hostname() == "" {
			return nil, ErrTransfer
		}
	}
	for hop := 0; hop <= 3; hop++ {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 16384 || (method == "PUT" && u.Scheme != "https") {
			return nil, ErrTransfer
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
		if err != nil || len(addresses) == 0 {
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			return nil, transferFailure(method, "域名解析", err)
		}
		for _, ip := range addresses {
			if !PublicAddress(ip) {
				return nil, ErrTransfer
			}
		}
		originalHost, serverName := u.Host, u.Hostname()
		// HTTP 代理绝对请求会用 Host 覆盖 URL 主机，只允许 HTTPS 隧道固定目标。
		if proxyURL != nil && u.Scheme != "https" {
			return nil, ErrTransfer
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		if proxyURL != nil {
			u.Host = net.JoinHostPort(addresses[0].Unmap().String(), port)
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DialContext: dialer.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}, TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: timeout, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 64 << 10}
		if proxyURL == nil {
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialAddresses(ctx, network, addresses, port, "", method)
			}
		}
		transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if proxyURL == nil {
				return dialAddresses(ctx, network, addresses, port, serverName, method)
			}
			name := serverName
			if proxyURL != nil && proxyURL.Scheme == "https" {
				name = proxyURL.Hostname()
			}
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			secured := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name})
			if err = secured.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return secured, nil
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
		if err != nil {
			return nil, ErrTransfer
		}
		req.Host = originalHost
		if method == "PUT" {
			req.Header.Set("Content-Type", media)
			req.Header.Set("x-ms-blob-type", "BlockBlob")
			req.Header.Set("x-ms-version", "2020-04-08")
			req.Header.Set("Origin", "https://chatgpt.com")
			req.Header.Set("Referer", "https://chatgpt.com/")
			req.Header.Set("User-Agent", "chatgpt2api image uploader")
			req.Header.Set("Accept", "application/json, text/plain, */*")
			req.Header.Set("Accept-Language", "en-US,en;q=0.8")
			if len(uploadHeaders) > 0 {
				for _, key := range []string{"Origin", "Referer", "User-Agent"} {
					if value := uploadHeaders[0].Get(key); value != "" {
						req.Header.Set(key, value)
					}
				}
			}
		}
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(req)
		if err != nil {
			transport.CloseIdleConnections()
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			var failure *Failure
			if errors.As(err, &failure) {
				return nil, failure
			}
			return nil, transferFailure(method, "请求传输", err)
		}
		if response.StatusCode >= 300 && response.StatusCode <= 399 {
			next, parseErr := url.Parse(response.Header.Get("Location"))
			response.Body.Close()
			transport.CloseIdleConnections()
			if method != "GET" || parseErr != nil || hop == 3 || next.String() == "" {
				return nil, ErrTransfer
			}
			base, _ := url.Parse(raw)
			next = base.ResolveReference(next)
			if base.Scheme == "https" && next.Scheme != "https" {
				return nil, ErrTransfer
			}
			raw = next.String()
			continue
		}
		limit := int64(MaxBytes)
		if method == "PUT" {
			limit = 1 << 20
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		response.Body.Close()
		transport.CloseIdleConnections()
		if readErr != nil || int64(len(body)) > limit || response.StatusCode < 200 || response.StatusCode >= 300 {
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			failure := transferFailure(method, "响应读取", readErr)
			failure.Status = response.StatusCode
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				failure.Phase, failure.Reason = "上游响应", "签名资源服务器拒绝请求"
				failure.Body = string(body[:min(len(body), 65536)])
			} else if int64(len(body)) > limit {
				failure.Reason = "响应超过大小限制"
			}
			return nil, failure
		}
		if method == "GET" && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
			return nil, ErrTransfer
		}
		return body, nil
	}
	return nil, ErrTransfer
}

// 仅在 TCP/TLS 建连期间尝试其他已验证地址；发送 HTTP 请求后绝不重放 PUT。
func dialAddresses(ctx context.Context, network string, addresses []netip.Addr, port, serverName, method string) (net.Conn, error) {
	var last error
	phase := "TCP 连接"
	for _, ip := range addresses {
		if ctx.Err() != nil {
			return nil, transferFailure(method, phase, ctx.Err())
		}
		attempt, stop := context.WithTimeout(ctx, 10*time.Second)
		dialer := &net.Dialer{}
		address := net.JoinHostPort(ip.Unmap().String(), port)
		conn, err := dialer.DialContext(attempt, network, address)
		phase = "TCP 连接"
		if err == nil && serverName != "" {
			phase = "TLS 握手"
			secured := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName})
			err = secured.HandshakeContext(attempt)
			if err != nil {
				conn.Close()
			} else {
				conn = secured
			}
		}
		stop()
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, transferFailure(method, phase, last)
}
