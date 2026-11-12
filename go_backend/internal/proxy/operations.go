package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ImportNode struct {
	URL                   string `json:"url"`
	ImageConcurrencyLimit int    `json:"image_concurrency_limit"`
}
type InvalidImport struct {
	Line   int    `json:"line"`
	Raw    string `json:"raw"`
	Reason string `json:"reason"`
}
type ImportResult struct {
	Nodes          []ImportNode    `json:"nodes"`
	AddedCount     int             `json:"added_count"`
	DuplicateCount int             `json:"duplicate_count"`
	InvalidCount   int             `json:"invalid_count"`
	InvalidItems   []InvalidImport `json:"invalid_items"`
}

// ImportNodes 解析 Python 支持的「代理地址 [图片并发]」逐行草稿格式。
// 命令返回的新节点仅用于提交草稿；无效行从不回显原始输入。
func (s *Service) ImportNodes(input string, existing []string) (ImportResult, error) {
	result := ImportResult{Nodes: []ImportNode{}, InvalidItems: []InvalidImport{}}
	if len(input) > 1000000 || len(existing) > 10000 {
		return result, ErrInput
	}
	seen := map[string]bool{}
	for _, raw := range existing {
		if normalized, err := NormalizeURL(raw); err == nil {
			seen[normalized] = true
		}
	}
	lines := strings.Split(input, "\n")
	count := 0
	for i, raw := range lines {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		count++
		if count > 10000 {
			return ImportResult{}, ErrInput
		}
		invalid := func(reason string) {
			result.InvalidItems = append(result.InvalidItems, InvalidImport{Line: i + 1, Raw: "[已隐藏]", Reason: reason})
		}
		if len(fields) > 2 {
			invalid("格式应为：代理地址 [图片并发]")
			continue
		}
		limit := 30
		if len(fields) == 2 {
			var err error
			limit, err = strconv.Atoi(fields[1])
			decimal := true
			for _, char := range fields[1] {
				if char < '0' || char > '9' {
					decimal = false
				}
			}
			if err != nil || !decimal || limit < 0 || limit > 10000 {
				invalid("图片并发必须是 0 到 10000 的整数")
				continue
			}
		}
		normalized, err := NormalizeURL(fields[0])
		if err != nil {
			invalid("请填写有效的 HTTP、HTTPS 或 SOCKS5 代理地址")
			continue
		}
		if seen[normalized] {
			result.DuplicateCount++
			continue
		}
		seen[normalized] = true
		result.Nodes = append(result.Nodes, ImportNode{URL: normalized, ImageConcurrencyLimit: limit})
	}
	result.AddedCount = len(result.Nodes)
	result.InvalidCount = len(result.InvalidItems)
	return result, nil
}

type TestResult struct {
	OK          bool    `json:"ok"`
	Status      int     `json:"status"`
	LatencyMS   int64   `json:"latency_ms"`
	Error       *string `json:"error"`
	ProxySource string  `json:"proxy_source"`
	HasProxy    bool    `json:"has_proxy"`
	ExitIP      string  `json:"exit_ip"`
	Country     string  `json:"country"`
	CheckedAt   string  `json:"checked_at"`
}
type NodeTestResult struct {
	NodeID string     `json:"node_id"`
	Result TestResult `json:"result"`
}
type TestSummary struct {
	Status       string `json:"status"`
	Tone         string `json:"tone"`
	Total        int    `json:"total"`
	Succeeded    int    `json:"succeeded"`
	Failed       int    `json:"failed"`
	MaxLatencyMS int64  `json:"max_latency_ms"`
	Label        string `json:"label"`
	Message      string `json:"message"`
}
type GroupTestResponse struct {
	Summary TestSummary      `json:"summary"`
	Results []NodeTestResult `json:"results"`
	Result  *TestResult      `json:"result"`
}

// Test 的目标固定，超时包括等待全局探测容量；错误只返回固定安全诊断。
func (s *Service) Test(parent context.Context, rawURL string) TestResult {
	started := time.Now()
	result := TestResult{ProxySource: "input", HasProxy: rawURL != ""}
	fail := func(message string) TestResult {
		result.Error = &message
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	normalized, err := NormalizeURL(rawURL)
	if err != nil {
		return fail(ErrInput.Error())
	}
	timeout := s.timeout
	if timeout > 15*time.Second {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	select {
	case s.probeSlots <- struct{}{}:
		defer func() { <-s.probeSlots }()
	case <-ctx.Done():
		return s.recordHealth(normalized, fail("代理探测已取消或超时"))
	}
	parsed, _ := url.Parse(normalized)
	transport := &http.Transport{Proxy: http.ProxyURL(parsed), DialContext: trustedProxyDial, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.probeTarget, nil)
	if err != nil {
		return fail("代理探测目标无效")
	}
	request.Header.Set("User-Agent", "chatgpt2api proxy test")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return s.recordHealth(normalized, fail("代理探测已取消或超时"))
		}
		return s.recordHealth(normalized, fail("代理连接或上游探测失败"))
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if len(body) > 64<<10 {
		return s.recordHealth(normalized, fail("代理探测响应超过大小限制"))
	}
	if err != nil {
		return s.recordHealth(normalized, fail("代理探测响应读取失败"))
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	result.OK = response.StatusCode >= 200 && response.StatusCode < 300
	if result.OK {
		// 只提取固定 trace 目标的出口信息，不回显其他响应内容。
		for _, line := range strings.Split(string(body), "\n") {
			key, value, found := strings.Cut(strings.TrimSpace(line), "=")
			if !found {
				continue
			}
			switch key {
			case "ip":
				if address, err := netip.ParseAddr(value); err == nil && address.IsGlobalUnicast() {
					result.ExitIP = address.Unmap().String()
				}
			case "loc":
				if len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z' {
					result.Country = value
				}
			}
		}
	}
	if !result.OK {
		message := fmt.Sprintf("代理探测返回 HTTP %d", response.StatusCode)
		result.Error = &message
	}
	return s.recordHealth(normalized, result)
}
func (s *Service) recordHealth(rawURL string, result TestResult) TestResult {
	checked := time.Now().UTC().Format(time.RFC3339Nano)
	result.CheckedAt = checked
	state := "unhealthy"
	if result.OK {
		state = "healthy"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 健康状态仅是当前进程的有界观察，不持久化配置副本。
	if len(s.health) >= 10000 {
		clear(s.health)
	}
	latency := result.LatencyMS
	s.health[rawURL] = Health{State: state, CheckedAt: &checked, LatencyMS: &latency, Error: result.Error, Status: result.Status, ExitIP: result.ExitIP, Country: result.Country}
	return result
}

// trustedProxyDial 每次解析并验证实际拨号 IP，禁止未指定、多播和链路本地地址。
// 管理员配置的回环/私网代理明确受信；不复用用户图片下载的公网策略。
func trustedProxyDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrUnavailable
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || !allowedProxyIP(address) {
			continue
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, ErrUnavailable
}
func allowedProxyIP(address netip.Addr) bool {
	return address.IsGlobalUnicast() || address.IsLoopback()
}

func (s *Service) TestActive(ctx context.Context, explicit string) (TestResult, error) {
	if explicit != "" {
		if _, err := NormalizeURL(explicit); err != nil {
			return TestResult{}, err
		}
		return s.Test(ctx, explicit), nil
	}
	resolved, err := s.Resolve(ctx, nil, nil)
	if err != nil {
		return TestResult{}, err
	}
	if resolved == "" {
		message := "当前默认出口为直连，没有可测试代理"
		return TestResult{ProxySource: "direct", Error: &message}, nil
	}
	result := s.Test(ctx, resolved)
	result.ProxySource = "default"
	return result, nil
}
func (s *Service) TestGroup(ctx context.Context, id, nodeID, explicit string) (GroupTestResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if explicit != "" {
		if _, err := NormalizeURL(explicit); err != nil {
			return GroupTestResponse{}, err
		}
		return summarize([]NodeTestResult{{Result: s.Test(ctx, explicit)}}), nil
	}
	raw, err := s.load(ctx)
	if err != nil {
		return GroupTestResponse{}, err
	}
	group := findGroup(raw, id)
	if group == nil {
		return GroupTestResponse{}, ErrNotFound
	}
	nodes := availableNodes(group)
	if nodeID != "" {
		selected := []map[string]any{}
		for _, node := range nodes {
			if text(node["id"]) == nodeID {
				selected = append(selected, node)
			}
		}
		nodes = selected
		if len(nodes) == 0 {
			return GroupTestResponse{}, ErrNotFound
		}
	}
	results := make([]NodeTestResult, len(nodes))
	var wait sync.WaitGroup
	// 至多四个工作协程；所有调用仍共享 Service 的全局探测信号量。
	jobs := make(chan int)
	for worker := 0; worker < 4 && worker < len(nodes); worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := range jobs {
				node := nodes[i]
				results[i] = NodeTestResult{NodeID: text(node["id"]), Result: s.Test(ctx, text(node["url"]))}
			}
		}()
	}
	for i := range nodes {
		jobs <- i
	}
	close(jobs)
	wait.Wait()
	return summarize(results), nil
}
func summarize(results []NodeTestResult) GroupTestResponse {
	if results == nil {
		results = []NodeTestResult{}
	}
	summary := TestSummary{Total: len(results), Status: "failed", Tone: "danger", Label: "代理组不可用"}
	for _, row := range results {
		if row.Result.OK {
			summary.Succeeded++
		}
		if row.Result.LatencyMS > summary.MaxLatencyMS {
			summary.MaxLatencyMS = row.Result.LatencyMS
		}
	}
	summary.Failed = summary.Total - summary.Succeeded
	if summary.Total > 0 && summary.Failed == 0 {
		summary.Status = "success"
		summary.Tone = "success"
		summary.Label = "代理组可用"
		summary.Message = fmt.Sprintf("%d 个节点全部可用，最慢 %dms", summary.Total, summary.MaxLatencyMS)
	} else if summary.Succeeded > 0 {
		summary.Status = "partial"
		summary.Tone = "warning"
		summary.Label = "代理组部分可用"
		summary.Message = fmt.Sprintf("%d 个节点中 %d 个可用，%d 个失败", summary.Total, summary.Succeeded, summary.Failed)
	} else if summary.Total > 0 {
		summary.Message = fmt.Sprintf("%d 个节点全部失败", summary.Total)
	} else {
		summary.Label = "代理组无可测试节点"
		summary.Message = "当前代理组没有可用节点"
	}
	response := GroupTestResponse{Summary: summary, Results: results}
	if len(results) == 1 {
		response.Result = &results[0].Result
	}
	return response
}

// Runtime 接收设置领域已经脱敏的 Proxy Session 投影，不把资源代理作为默认出口。
func (s *Service) Runtime(ctx context.Context, runtime map[string]any) (map[string]any, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	ref := parseReference(text(raw["proxy"]), true)
	effective := s.effective(raw, ref)
	copied := clone(runtime).(map[string]any)
	copied["reset_session_status_codes"] = []int{403}
	clearance, _ := copied["clearance"].(map[string]any)
	if clearance == nil {
		clearance = map[string]any{}
		copied["clearance"] = clearance
	}
	clearance["browser"] = "chrome"
	status := map[string]any{"enabled": copied["enabled"] == true, "proxy_source": effective["source"], "has_proxy": effective["has_proxy"], "skip_ssl_verify": copied["skip_ssl_verify"] == true, "clearance_enabled": clearance["enabled"] == true, "clearance_mode": or(text(clearance["mode"]), "none"), "has_clearance_bundle": false, "cached_clearance_hosts": []string{}, "supported": false, "label": "代理会话附加行为尚未启用"}
	return map[string]any{"runtime": copied, "status": status}, nil
}
func (s *Service) TestClearance(ctx context.Context, target string, runtime map[string]any) (map[string]any, error) {
	// 尚未实现真实挑战求解；不将配置中的 cookie 存在视为挑战成功。
	if target == "" {
		target = "https://chatgpt.com"
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host != "chatgpt.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInput
	}
	projection, err := s.Runtime(ctx, runtime)
	if err != nil {
		return nil, err
	}
	message := "Go 后端尚不支持通行挑战求解"
	return map[string]any{"ok": false, "status": "unsupported", "latency_ms": 0, "has_cookies": false, "user_agent": "", "error": message, "runtime": projection["status"]}, nil
}
