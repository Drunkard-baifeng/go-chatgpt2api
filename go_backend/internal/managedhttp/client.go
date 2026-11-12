// Package managedhttp 为管理员显式配置的集成端点提供固定来源、实际拨号校验与有界传输。
package managedhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrTarget = errors.New("集成服务地址或路径无效")
var ErrRequest = errors.New("集成服务请求失败")

type Client struct {
	base      *url.URL
	client    *http.Client
	transport *http.Transport
}

func allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && ip != netip.MustParseAddr("255.255.255.255") && ip != netip.MustParseAddr("100.100.100.200")
}
func ParseURL(raw string) (*url.URL, error) {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return nil, ErrTarget
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(u.Hostname(), "%") {
		return nil, ErrTarget
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && !allowed(ip) {
		return nil, ErrTarget
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, ErrTarget
		}
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == ".." || segment == "." {
			return nil, ErrTarget
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// 私网和回环只用于管理员显式选择的集成来源。每次实际拨号检查所有DNS结果并固定IP。
func New(raw string) (*Client, error) {
	u, e := ParseURL(raw)
	if e != nil {
		return nil, e
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 4, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || !strings.EqualFold(host, u.Hostname()) {
			return nil, ErrRequest
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil || len(ips) == 0 {
			return nil, ErrRequest
		}
		for _, ip := range ips {
			if !allowed(ip) {
				return nil, ErrTarget
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if e == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, ErrRequest
	}
	return &Client{base: u, transport: transport, client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }
func (c *Client) Request(parent context.Context, method, path string, body io.Reader, headers http.Header, limit int64) ([]byte, int, error) {
	if limit < 0 || limit > 512<<20 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\x00\r\n\\") {
		return nil, 0, ErrTarget
	}
	part, e := url.ParseRequestURI(path)
	if e != nil || part.IsAbs() || part.Host != "" || part.Fragment != "" {
		return nil, 0, ErrTarget
	}
	for _, s := range strings.Split(part.Path, "/") {
		if s == ".." || s == "." {
			return nil, 0, ErrTarget
		}
	}
	u := *c.base
	u.Path += part.Path
	u.RawPath = ""
	u.RawQuery = part.RawQuery
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, u.String(), body)
	if e != nil {
		return nil, 0, ErrTarget
	}
	req.GetBody = nil
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	res, e := c.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, ErrRequest
	}
	defer res.Body.Close()
	if res.ContentLength > limit {
		return nil, res.StatusCode, ErrRequest
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if ctx.Err() != nil {
		return nil, res.StatusCode, ctx.Err()
	}
	if e != nil || int64(len(data)) > limit {
		return nil, res.StatusCode, ErrRequest
	}
	return data, res.StatusCode, nil
}
