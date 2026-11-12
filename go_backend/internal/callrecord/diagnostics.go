package callrecord

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 诊断只接收结构化元信息；不接收请求正文、响应正文、请求头或签名地址。
type RequestMetadata struct {
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
	N              int    `json:"n,omitempty"`
	Stream         bool   `json:"stream"`
}
type ResultImage struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	URL    string `json:"url,omitempty"`
}
type DiagnosticEvent struct {
	Count                int    `json:"count,omitempty"`
	Time                 string `json:"time"`
	StartedAt            string `json:"started_at,omitempty"`
	Event                string `json:"event"`
	Stage                string `json:"stage,omitempty"`
	Metric               string `json:"metric,omitempty"`
	DurationMS           int64  `json:"duration_ms"`
	Status               string `json:"status,omitempty"`
	ErrorCode            string `json:"error_code,omitempty"`
	StatusCode           int    `json:"status_code,omitempty"`
	UpstreamRequestID    string `json:"upstream_request_id,omitempty"`
	Slot                 int    `json:"slot,omitempty"`
	Attempt              int    `json:"attempt,omitempty"`
	AccountEmail         string `json:"account_email,omitempty"`
	PreviousAccountEmail string `json:"previous_account_email,omitempty"`
	Egress               string `json:"egress,omitempty"`
	SwitchedAccount      bool   `json:"switched_account,omitempty"`
}
type Attempt struct {
	Slot                 int              `json:"slot"`
	Attempt              int              `json:"attempt"`
	AccountID            string           `json:"account_id"`
	AccountEmail         string           `json:"account_email"`
	PreviousAccountEmail string           `json:"previous_account_email,omitempty"`
	Egress               string           `json:"egress"`
	StartedAt            string           `json:"started_at"`
	EndedAt              string           `json:"ended_at"`
	Status               string           `json:"status"`
	ErrorCode            string           `json:"error_code"`
	PublicError          string           `json:"public_error"`
	UpstreamError        string           `json:"upstream_error,omitempty"`
	UpstreamText         string           `json:"upstream_text,omitempty"`
	RawError             string           `json:"raw_error,omitempty"`
	APIStatusCode        int              `json:"api_status_code,omitempty"`
	StatusCode           int              `json:"status_code"`
	UpstreamRequestID    string           `json:"upstream_request_id"`
	Stage                string           `json:"stage"`
	ConversationID       string           `json:"conversation_id"`
	SwitchedAccount      bool             `json:"switched_account"`
	Timings              map[string]int64 `json:"timings_ms"`
}
type AttemptResult struct {
	Status, ErrorCode, PublicError, Stage, ConversationID string
	UpstreamError, UpstreamText, RawError                 string
	APIStatusCode                                         int
	EndedAt                                               time.Time
}
type Diagnostics struct {
	RequestMeta       RequestMetadata
	RequestShape      map[string]int
	ResultImages      []ResultImage
	Attempts          []Attempt
	Events            []DiagnosticEvent
	Timings           map[string]int64
	Egress            string
	StatusCode        int
	UpstreamRequestID string
}

// Trace 属于一次调用，只拥有有界诊断快照，不另启请求生命周期或持久化。
type Trace struct {
	mu        sync.RWMutex
	data      Diagnostics
	spans     map[string]*metricSpan
	nextSpan  uint64
	liveSpans map[uint64]liveSpan
}
type liveSpan struct {
	index   int
	metric  string
	started time.Time
}
type metricSpan struct {
	active  int
	started time.Time
	elapsed time.Duration
}
type traceKey struct{}
type traceScope struct {
	trace *Trace
	index int
}

func NewTrace(meta RequestMetadata, shape map[string]int) *Trace {
	meta.Size = SafeDiagnosticText(meta.Size)
	meta.Quality = SafeDiagnosticText(meta.Quality)
	meta.ResponseFormat = SafeDiagnosticText(meta.ResponseFormat)
	meta.OutputFormat = SafeDiagnosticText(meta.OutputFormat)
	return &Trace{data: Diagnostics{RequestMeta: meta, RequestShape: maps.Clone(shape), Timings: map[string]int64{}, Attempts: []Attempt{}, Events: []DiagnosticEvent{}, ResultImages: []ResultImage{}}, spans: map[string]*metricSpan{}, liveSpans: map[uint64]liveSpan{}}
}
func WithTrace(ctx context.Context, trace *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, traceScope{trace: trace, index: -1})
}
func scope(ctx context.Context) traceScope {
	s, ok := ctx.Value(traceKey{}).(traceScope)
	if !ok {
		s.index = -1
	}
	return s
}

var diagnosticSecrets = regexp.MustCompile(`(?i)(?:authorization["']?(?:\s*[:=]\s*|\s+)(?:(?:bearer|basic)\s+)?|(?:access_token|refresh_token|id_token|api_key|password)["']?(?:\s*[:=]\s*|\s+)|\bbearer\s+)(?:"(?:\\.|[^"\\])*"?|'(?:\\.|[^'\\])*'?|[^\s,;"'}]+)|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\brt\.[A-Za-z0-9._-]+`)
var diagnosticIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
var diagnosticCookies = regexp.MustCompile(`(?im)(?:^|\n)[ \t]*(?:set-cookie|cookie)\s*:\s*[^\r\n]*|(?:"|')?(?:cookie|set-cookie|client_secret|secret)(?:"|')?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}]+)`)
var diagnosticURLs = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var diagnosticData = regexp.MustCompile(`(?i)data:[^\s,;]+(?:;[^,]*)?,[^\s"'<>]+`)
var diagnosticProtocolSecrets = regexp.MustCompile(`(?i)(?:\b(?:x[_-]conduit[_-]token|prepare[_-]token|conduit[_-]token|proof[_-]token|turnstile[_-]token)["']?(?:\s*[:=]\s*|\s+)|\btoken["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"?|'(?:\\.|[^'\\])*'?|[^\s,;"'}]+)`)

// CaptureDiagnosticText 保留上游原因和换行；仅保存最多 4000 字符的脱敏摘录。
func CaptureDiagnosticText(value string) string {
	// 上游正文已有传输大小限制，这里再限制脱敏扫描开销。
	if len(value) > 65536 {
		value = value[:65536]
	}
	value = redactDiagnosticContent(value, 0)
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	runes := []rune(value)
	if len(runes) > 4000 {
		return string(runes[:3999]) + "…"
	}
	return value
}

func redactDiagnosticContent(value string, depth int) string {
	if depth > 32 {
		return "[诊断内容过深]"
	}
	// 先按解码后的字段名脱敏，覆盖嵌套对象、转义 key 和非字符串凭据。
	if json.Valid([]byte(value)) {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) == nil {
			if data, err := json.Marshal(redactDiagnosticValue(decoded, depth+1)); err == nil {
				return string(data)
			}
		}
	}
	value = diagnosticCookies.ReplaceAllString(value, "\n[凭据已隐藏]")
	value = diagnosticSecrets.ReplaceAllString(value, "[已隐藏]")
	value = diagnosticProtocolSecrets.ReplaceAllString(value, "[已隐藏]")
	value = diagnosticData.ReplaceAllString(value, "[图片数据已隐藏]")
	value = diagnosticURLs.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "[签名或认证地址已隐藏]"
		}
		return raw
	})
	return value
}

func redactDiagnosticValue(value any, depth int) any {
	if depth > 32 {
		return "[诊断内容过深]"
	}
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
			case "authorization", "cookie", "set_cookie", "client_secret", "secret", "access_token", "refresh_token", "id_token", "api_key", "password", "token", "prepare_token", "conduit_token", "x_conduit_token", "proof_token", "turnstile_token":
				v[key] = "[已隐藏]"
			default:
				v[key] = redactDiagnosticValue(item, depth+1)
			}
		}
	case []any:
		for i, item := range v {
			v[i] = redactDiagnosticValue(item, depth+1)
		}
	case string:
		return redactDiagnosticContent(v, depth+1)
	}
	return value
}

func SafeDiagnosticText(value string) string {
	value = diagnosticSecrets.ReplaceAllString(strings.TrimSpace(value), "[已隐藏]")
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000]) + "…"
	}
	return value
}

// CaptureRequestText 保存输入文字及换行；与 Python 一样最多保留 50000 字符。
// 它不接受请求头或图片数据，已识别的凭据在存储前隐藏。
func CaptureRequestText(value string) (string, bool) {
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	value = diagnosticSecrets.ReplaceAllString(value, "[已隐藏]")
	runes := []rune(value)
	if len(runes) > 50000 {
		return string(runes[:49999]) + "…", true
	}
	return value, false
}

// ResultImageURL 只允许本服务公开图片路径，不保存上游签名或认证信息。
func ResultImageURL(raw string) string {
	if len(raw) > 8192 || strings.ContainsAny(raw, "?#\\\r\n\t ") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return ""
	}
	if u.IsAbs() {
		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return ""
		}
	} else if u.Host != "" || !strings.HasPrefix(u.Path, "/images/") && !strings.HasPrefix(u.Path, "/image-thumbnails/") {
		return ""
	}
	if !strings.Contains(u.Path, "/images/") && !strings.Contains(u.Path, "/image-thumbnails/") {
		return ""
	}
	if strings.ContainsAny(u.Path, "?#\\\r\n\t%") {
		return ""
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return ""
		}
	}
	return raw
}
func SafeEgress(proxy string) string {
	if proxy == "" {
		return "直连"
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Hostname() == "" {
		return "代理出口"
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u.Scheme + "://" + u.Host
	default:
		return "代理出口"
	}
}

func (t *Trace) BeginAttempt(ctx context.Context, slot, number int, id, email, previous, proxy string, started time.Time) context.Context {
	if t == nil {
		return ctx
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.data.Attempts) >= 32 {
		return context.WithValue(ctx, traceKey{}, traceScope{trace: t, index: -1})
	}
	a := Attempt{Slot: slot, Attempt: number, AccountID: SafeDiagnosticText(id), AccountEmail: safeAccountEmail(email), PreviousAccountEmail: safeAccountEmail(previous), Egress: SafeEgress(proxy), StartedAt: started.UTC().Format(time.RFC3339Nano), Status: "running", SwitchedAccount: number > 1, Timings: map[string]int64{}}
	a.Timings["account_wait_ms"] = max(0, time.Since(started).Milliseconds())
	t.data.Attempts = append(t.data.Attempts, a)
	t.data.Egress = a.Egress
	s := traceScope{trace: t, index: len(t.data.Attempts) - 1}
	t.eventLocked(s, DiagnosticEvent{Event: "attempt_started", Stage: "account_selected", Status: "running"})
	return context.WithValue(ctx, traceKey{}, s)
}
func (t *Trace) EndAttempt(ctx context.Context, result AttemptResult) {
	if t == nil {
		return
	}
	s := scope(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	if s.trace != t || s.index < 0 || s.index >= len(t.data.Attempts) {
		return
	}
	a := &t.data.Attempts[s.index]
	end := result.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	a.EndedAt = end.UTC().Format(time.RFC3339Nano)
	a.Status = result.Status
	a.ErrorCode = SafeDiagnosticText(result.ErrorCode)
	a.PublicError = SafeDiagnosticText(result.PublicError)
	a.UpstreamError = CaptureDiagnosticText(result.UpstreamError)
	a.UpstreamText = CaptureDiagnosticText(result.UpstreamText)
	a.RawError = CaptureDiagnosticText(result.RawError)
	a.APIStatusCode = result.APIStatusCode
	if result.Stage != "" {
		a.Stage = SafeDiagnosticText(result.Stage)
	}
	a.ConversationID = SafeDiagnosticText(result.ConversationID)
	t.eventLocked(s, DiagnosticEvent{Time: a.EndedAt, Event: "attempt_finished", Stage: a.Stage, Status: a.Status, ErrorCode: a.ErrorCode, StatusCode: a.StatusCode, UpstreamRequestID: a.UpstreamRequestID})
}
func (t *Trace) SetResults(images []ResultImage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.ResultImages = append([]ResultImage{}, images...)
	for i := range t.data.ResultImages {
		image := &t.data.ResultImages[i]
		image.URL = ResultImageURL(image.URL)
	}
}
func RecordEvent(ctx context.Context, event DiagnosticEvent) {
	s := scope(ctx)
	if s.trace == nil {
		return
	}
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	s.trace.eventLocked(s, event)
}
func (t *Trace) eventLocked(s traceScope, event DiagnosticEvent) {
	if event.Time == "" {
		event.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	event.Event = SafeDiagnosticText(event.Event)
	event.Stage = SafeDiagnosticText(event.Stage)
	event.ErrorCode = SafeDiagnosticText(event.ErrorCode)
	if !diagnosticIdentifier.MatchString(event.UpstreamRequestID) {
		event.UpstreamRequestID = ""
	}
	if s.index >= 0 && s.index < len(t.data.Attempts) {
		a := &t.data.Attempts[s.index]
		if event.Stage != "" {
			a.Stage = event.Stage
		}
		event.Slot = a.Slot
		event.Attempt = a.Attempt
		event.AccountEmail = a.AccountEmail
		event.PreviousAccountEmail = a.PreviousAccountEmail
		event.Egress = a.Egress
		event.SwitchedAccount = a.SwitchedAccount
		if event.StatusCode > 0 {
			a.StatusCode = event.StatusCode
		}
		if event.UpstreamRequestID != "" {
			a.UpstreamRequestID = event.UpstreamRequestID
		}
	}
	if event.StatusCode > 0 {
		t.data.StatusCode = event.StatusCode
	}
	if event.UpstreamRequestID != "" {
		t.data.UpstreamRequestID = event.UpstreamRequestID
	}
	if (event.Metric == "poll_wait_ms" || event.Metric == "poll_request_ms") && strings.HasSuffix(event.Event, "_finished") {
		for i := len(t.data.Events) - 1; i >= 0; i-- {
			previous := t.data.Events[i]
			if previous.Event == event.Stage+"_started" && previous.Slot == event.Slot && previous.Attempt == event.Attempt {
				t.data.Events = append(t.data.Events[:i], t.data.Events[i+1:]...)
				break
			}
		}
	}
	// 轮询是同一阶段的重复工作；保存有效耗时、次数和观测范围，避免挤掉准备阶段。
	if (event.Metric == "poll_wait_ms" || event.Metric == "poll_request_ms") && strings.HasSuffix(event.Event, "_finished") && event.ErrorCode == "" && (event.Status == "success" || event.Status == "") {
		if event.StartedAt == "" {
			if end, err := time.Parse(time.RFC3339Nano, event.Time); err == nil {
				event.StartedAt = end.Add(-time.Duration(event.DurationMS) * time.Millisecond).UTC().Format(time.RFC3339Nano)
			}
		}
		event.Count = max(1, event.Count)
		for i := range t.data.Events {
			previous := &t.data.Events[i]
			if previous.Event != event.Event || previous.Slot != event.Slot || previous.Attempt != event.Attempt || previous.ErrorCode != "" || (previous.Status != "success" && previous.Status != "") {
				continue
			}
			previous.Count = max(1, previous.Count) + event.Count
			previous.DurationMS += event.DurationMS
			previous.Time = event.Time
			return
		}
	}
	if len(t.data.Events) == 128 {
		// 优先淘汰底层回调；成功 HTTP/SSE 事件不能淘汰已有业务历史。
		evict := 0
		transport := func(e DiagnosticEvent) bool {
			return (strings.HasPrefix(e.Event, "http_") || strings.HasPrefix(e.Event, "sse_")) && e.Status != "failed" && e.ErrorCode == ""
		}
		for i, previous := range t.data.Events {
			if transport(previous) {
				evict = i
				break
			}
		}
		if transport(event) && !transport(t.data.Events[evict]) {
			return
		}
		copy(t.data.Events[evict:], t.data.Events[evict+1:])
		t.data.Events = t.data.Events[:127]
	}
	t.data.Events = append(t.data.Events, event)
}
func RecordMetric(ctx context.Context, key string, ms int64) {
	if ms < 0 || len(key) > 64 || !strings.HasSuffix(key, "_ms") {
		return
	}
	s := scope(ctx)
	if s.trace == nil {
		return
	}
	t := s.trace
	t.mu.Lock()
	defer t.mu.Unlock()
	apply := func(metrics map[string]int64) {
		if strings.HasPrefix(key, "http_") || strings.HasPrefix(key, "sse_") || key == "stream_error_ms" {
			metrics[key] = max(metrics[key], ms)
		} else {
			metrics[key] += ms
		}
	}
	apply(t.data.Timings)
	if s.index >= 0 && s.index < len(t.data.Attempts) {
		apply(t.data.Attempts[s.index].Timings)
	}
}

// Span 记录实际进入/退出阶段，调用方只能传应用定义的阶段名和安全错误分类。
func Span(ctx context.Context, stage, metric string) func(error) {
	started := time.Now()
	s := scope(ctx)
	var spanID uint64
	if s.trace != nil && metric != "" {
		s.trace.mu.Lock()
		m := s.trace.spans[metric]
		if m == nil {
			m = &metricSpan{}
			s.trace.spans[metric] = m
		}
		if m.active == 0 {
			m.started = started
		}
		m.active++
		s.trace.nextSpan++
		spanID = s.trace.nextSpan
		s.trace.liveSpans[spanID] = liveSpan{index: s.index, metric: metric, started: started}
		s.trace.mu.Unlock()
	}
	RecordEvent(ctx, DiagnosticEvent{Event: stage + "_started", Stage: stage, Status: "running"})
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			ms := max(0, time.Since(started).Milliseconds())
			if s.trace != nil && metric != "" {
				t := s.trace
				t.mu.Lock()
				m := t.spans[metric]
				m.active--
				delete(t.liveSpans, spanID)
				if m.active == 0 {
					m.elapsed += time.Since(m.started)
					t.data.Timings[metric] = m.elapsed.Milliseconds()
				}
				if s.index >= 0 && s.index < len(t.data.Attempts) {
					t.data.Attempts[s.index].Timings[metric] += ms
				}
				t.mu.Unlock()
			}
			status := "success"
			code := ""
			if err != nil {
				status = "failed"
				if errors.Is(err, context.Canceled) {
					status = "cancelled"
					code = "cancelled"
				} else if errors.Is(err, context.DeadlineExceeded) {
					code = "timeout"
				}
			}
			RecordEvent(ctx, DiagnosticEvent{Event: stage + "_finished", Stage: stage, StartedAt: started.UTC().Format(time.RFC3339Nano), Metric: metric, DurationMS: ms, Status: status, ErrorCode: code})
		})
	}
}
func (t *Trace) Snapshot() Diagnostics {
	if t == nil {
		return Diagnostics{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := t.data
	result.RequestShape = maps.Clone(result.RequestShape)
	result.Timings = maps.Clone(result.Timings)
	for key, m := range t.spans {
		if m.active > 0 {
			result.Timings[key] = (m.elapsed + time.Since(m.started)).Milliseconds()
		}
	}
	result.Attempts = append([]Attempt{}, result.Attempts...)
	for i := range result.Attempts {
		result.Attempts[i].Timings = maps.Clone(result.Attempts[i].Timings)
	}
	for _, span := range t.liveSpans {
		if span.index >= 0 && span.index < len(result.Attempts) {
			result.Attempts[span.index].Timings[span.metric] += max(0, time.Since(span.started).Milliseconds())
		}
	}
	result.Events = append([]DiagnosticEvent{}, result.Events...)
	result.ResultImages = append([]ResultImage{}, result.ResultImages...)
	return result
}
