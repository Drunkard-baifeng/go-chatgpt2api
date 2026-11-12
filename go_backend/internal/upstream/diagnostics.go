package upstream

import (
	"bufio"
	"context"
	"errors"
	"io"
	nettrace "net/http/httptrace"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
	"github.com/bogdanfinn/fhttp/httptrace"
	tls "github.com/bogdanfinn/utls"
)

// HTTP callbacks may race with cancellation and may arrive after Body.Close.
// Keep their timing state separate from the request's execution lifecycle.
type httpDiagnostics struct {
	mu                       sync.Mutex
	ctx                      context.Context
	started, dns, tls, wrote time.Time
	connect                  map[string]time.Time
	observed                 map[string]bool
	finished                 bool
	status                   int
	requestID                string
}

func beginHTTPDiagnostics(ctx context.Context) (context.Context, *httpDiagnostics) {
	d := &httpDiagnostics{ctx: ctx, started: time.Now(), connect: map[string]time.Time{}, observed: map[string]bool{}}
	callrecord.RecordEvent(ctx, callrecord.DiagnosticEvent{Event: "http_started", Stage: "http_request", Status: "running"})
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.finished {
				d.dns = time.Now()
			}
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.phase("http_dns_ms", d.dns, info.Err)
			d.dns = time.Time{}
		},
		ConnectStart: func(network, address string) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.finished {
				d.connect[network+"\x00"+address] = time.Now()
			}
		},
		ConnectDone: func(network, address string, err error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			key := network + "\x00" + address
			d.phase("http_tcp_ms", d.connect[key], err)
			delete(d.connect, key)
		},
		TLSHandshakeStart: func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.finished {
				d.tls = time.Now()
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.phase("http_tls_ms", d.tls, err)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.finished && info.Err == nil {
				d.wrote = time.Now()
			}
		},
		GotFirstResponseByte: func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.phase("http_ttfb_ms", d.started, nil)
			d.phase("http_wait_ms", d.wrote, nil)
		},
	}
	// fhttp owns response callbacks; the pinned transport's standard net.Dialer
	// reads the standard library's separate network trace context key.
	ctx = nettrace.WithClientTrace(ctx, &nettrace.ClientTrace{
		DNSStart:     func(info nettrace.DNSStartInfo) { trace.DNSStart(httptrace.DNSStartInfo{Host: info.Host}) },
		DNSDone:      func(info nettrace.DNSDoneInfo) { trace.DNSDone(httptrace.DNSDoneInfo{Err: info.Err}) },
		ConnectStart: trace.ConnectStart,
		ConnectDone:  trace.ConnectDone,
	})
	return httptrace.WithClientTrace(ctx, trace), d
}

// phase is called with mu held. An absent callback stays absent rather than
// fabricating a zero duration for transport internals the pinned library hides.
func (d *httpDiagnostics) phase(metric string, started time.Time, err error) {
	if d.finished || started.IsZero() {
		return
	}
	ms := max(0, time.Since(started).Milliseconds())
	d.observed[metric] = true
	callrecord.RecordMetric(d.ctx, metric, ms)
	status, code := diagnosticStatus(err)
	callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "http_phase_finished", Stage: metric[:len(metric)-3], Metric: metric, DurationMS: ms, Status: status, ErrorCode: code})
}

func (d *httpDiagnostics) headers(status int, requestID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	d.status, d.requestID = status, requestID
	callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "http_headers_received", Stage: "http_request", DurationMS: max(0, time.Since(d.started).Milliseconds()), Status: "running", StatusCode: status, UpstreamRequestID: requestID})
}

func (d *httpDiagnostics) finish(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	if !d.observed["http_wait_ms"] && !d.wrote.IsZero() {
		d.phase("http_wait_ms", d.wrote, err)
	}
	d.finished = true
	ms := max(0, time.Since(d.started).Milliseconds())
	callrecord.RecordMetric(d.ctx, "http_total_ms", ms)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err == nil && d.status != 0 {
		err = checkStatus(d.status, "http_request")
	}
	status, code := diagnosticStatus(err)
	callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "http_finished", Stage: "http_request", Metric: "http_total_ms", DurationMS: ms, Status: status, ErrorCode: code, StatusCode: d.status, UpstreamRequestID: d.requestID})
	missing := 0
	for _, metric := range []string{"http_dns_ms", "http_tcp_ms", "http_tls_ms"} {
		if !d.observed[metric] {
			missing++
		}
	}
	if missing != 0 {
		callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "http_trace_unavailable", Stage: "http_connection_details", Status: "unavailable", ErrorCode: "transport_trace_unavailable", Count: missing})
	}
}

func diagnosticStatus(err error) (string, string) {
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return "success", ""
	case errors.Is(err, context.Canceled):
		return "cancelled", "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "failed", "timeout"
	case errors.Is(err, ErrResponseTooLarge):
		return "failed", "response_too_large"
	case errors.Is(err, ErrProtocol):
		return "failed", "upstream_protocol_error"
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return "failed", failure.Code
	}
	return "failed", "upstream_error"
}

type sseDiagnostics struct {
	ctx                context.Context
	stage              string
	started, last      time.Time
	count              int
	maxGap             int64
	finishSpan         func(error)
	finishConversation func(error)
	interrupted        bool
}

func waitForPoll(ctx context.Context, interval time.Duration, stage string) (err error) {
	finish := callrecord.Span(ctx, stage, "poll_wait_ms")
	defer func() { finish(err) }()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func beginSSEDiagnostics(ctx context.Context, kind string) *sseDiagnostics {
	now := time.Now()
	return &sseDiagnostics{ctx: ctx, stage: kind + "_stream", started: now, last: now, finishSpan: callrecord.Span(ctx, kind+"_stream", "stream_ms"), finishConversation: callrecord.Span(ctx, "conversation_stream", "conversation_stream_ms")}
}

// 仅在流读取失败处调用；业务拒绝、无效消息和下游写入失败不属于断流。
func (d *sseDiagnostics) transportError(err error) {
	if d.interrupted || err == nil || errors.Is(err, context.Canceled) || errors.Is(d.ctx.Err(), context.Canceled) || errors.Is(err, bufio.ErrTooLong) || errors.Is(err, ErrResponseTooLarge) {
		return
	}
	d.interrupted = true
	callrecord.RecordMetric(d.ctx, "stream_error_ms", max(0, time.Since(d.started).Milliseconds()))
}
func (d *sseDiagnostics) event() {
	now := time.Now()
	gap := max(0, now.Sub(d.last).Milliseconds())
	d.maxGap = max(d.maxGap, gap)
	if d.count == 0 {
		callrecord.RecordMetric(d.ctx, "sse_first_event_ms", max(0, now.Sub(d.started).Milliseconds()))
		callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "sse_first_event", Stage: d.stage, Metric: "sse_first_event_ms", DurationMS: max(0, now.Sub(d.started).Milliseconds()), Status: "running"})
	}
	d.count++
	d.last = now
}
func (d *sseDiagnostics) finish(err error) {
	if err == nil {
		err = d.ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		d.transportError(err)
	}
	lastGap := max(0, time.Since(d.last).Milliseconds())
	callrecord.RecordMetric(d.ctx, "sse_max_gap_ms", d.maxGap)
	callrecord.RecordMetric(d.ctx, "sse_last_gap_ms", lastGap)
	status, code := diagnosticStatus(err)
	callrecord.RecordEvent(d.ctx, callrecord.DiagnosticEvent{Event: "sse_finished", Stage: d.stage, Metric: "stream_ms", DurationMS: max(0, time.Since(d.started).Milliseconds()), Count: d.count, Status: status, ErrorCode: code})
	d.finishSpan(err)
	d.finishConversation(err)
}
