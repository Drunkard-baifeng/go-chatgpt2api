package observability

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
)

func monitorEvent(record callrecord.Record, event, label string, at time.Time) map[string]any {
	ms := int64(0)
	if event == "request_finished" {
		ms = max(0, at.Sub(record.Started).Milliseconds())
	}
	return map[string]any{"time": stamp(at), "call_id": record.ID, "event": event, "label": label, "stage": record.Stage, "detail_text": stageLabel(record.Stage), "duration_ms": ms, "timing_text": duration(ms), "model": text(record.Model)}
}

func formatWindow(count int) string { return fmt.Sprintf("窗口 %d / %d", count, recentCapacity) }

func traceDetail(record callrecord.Record) map[string]any {
	if record.Trace == nil {
		return map[string]any{}
	}
	d := record.Trace.Snapshot()
	data, _ := json.Marshal(map[string]any{"request_meta": d.RequestMeta, "request_shape": d.RequestShape, "result_images": d.ResultImages, "attempts": d.Attempts, "metrics": d.Timings, "monitor": map[string]any{"events": d.Events, "metrics": d.Timings}, "egress": d.Egress, "status_code": d.StatusCode, "upstream_request_id": d.UpstreamRequestID})
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func projectMonitor(tracked trackedCall, now time.Time, includeDetail bool, publicBaseURLs ...string) map[string]any {
	r := tracked.record
	end := r.Ended
	if end.IsZero() {
		end = now
	}
	ms := max(0, end.Sub(r.Started).Milliseconds())
	status := outcome(r.Outcome, r.ErrorCode)
	present := statusPresentation(status)
	detail := traceDetail(r)
	monitor := safeMonitor(detail["monitor"])
	events := array(monitor["events"])
	attempts := projectAttempts(detail["attempts"], events)
	coarse := map[string]any{}
	for key, value := range r.Timings {
		coarse[key] = value
	}
	if r.Ended.IsZero() {
		for key, value := range tracked.timings {
			coarse[key] = value
		}
		if metric := stageMetric(r.Stage); metric != "" && !tracked.stageStarted.IsZero() {
			coarse[metric] = integer(coarse[metric]) + max(0, now.Sub(tracked.stageStarted).Milliseconds())
		}
	}
	timings := safeMetrics(coarse)
	for key, value := range safeMetrics(detail["metrics"]) {
		timings[key] = value
	}
	timings["total_ms"] = ms
	businessEvents := monitorBusinessEvents(tracked, events, now)
	stage := r.Stage
	stageAt := tracked.stageStarted
	for _, event := range businessEvents {
		e := object(event)
		if stageTimingMetric(text(e["stage"])) == "" || strings.HasPrefix(text(e["event"]), "http_") || strings.HasPrefix(text(e["event"]), "sse_") {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, text(e["time"]))
		if candidate := text(e["stage"]); candidate != "" && !at.Before(stageAt) {
			stage = candidate
			stageAt = at
		}
	}
	if r.Ended.IsZero() {
		var runningAt time.Time
		for _, raw := range businessEvents {
			event := object(raw)
			if !mainMetric(text(event["metric"])) || !strings.HasSuffix(text(event["event"]), "_started") {
				continue
			}
			at, _ := time.Parse(time.RFC3339Nano, text(event["time"]))
			if !at.Before(runningAt) {
				stage, runningAt = text(event["stage"]), at
			}
		}
	}
	if !r.Ended.IsZero() && (status == "success" || status == "partial_success") {
		stage = "completed"
	}
	stageText := stageLabel(stage)
	if stageText == "" {
		stageText = text(present["label"])
	}
	email, accountID, previous, egress := text(r.AccountEmail), text(r.AccountID), "", safeEgress(detail["egress"])
	switches := max(0, r.Switches)
	derivedSwitches := 0
	for _, item := range attempts {
		if object(item)["switched_account"] == true {
			derivedSwitches++
		}
	}
	switches = max(switches, derivedSwitches)
	if len(attempts) > 0 {
		// Trace order is chronological; the display ordering of attempt groups is
		// by slot. Use the last diagnostic attempt for the current account.
		rawAttempts := array(detail["attempts"])
		latest := object(rawAttempts[len(rawAttempts)-1])
		email = first(latest["account_email"], email)
		accountID = first(latest["account_id"], accountID)
		previous = text(latest["previous_account_email"])
	}
	if egress == "" {
		egress = "未记录出口"
	}
	metricItems, trackedMS := []any{}, int64(0)
	for _, segment := range array(timeline(timings, events, coarse)["segments"]) {
		trackedMS += integer(object(segment)["value_ms"])
	}
	trackedMS = min(ms, trackedMS)
	mainKeys := selectedMetricKeys(timings)
	for _, spec := range timingSpecs {
		value := integer(timings[spec.key])
		if !mainKeys[spec.key] || value == 0 {
			continue
		}
		metricItems = append(metricItems, map[string]any{"key": spec.key, "label": spec.label, "value_ms": value, "value_text": duration(value), "important": value >= 10000})
	}
	if untracked := ms - trackedMS; untracked >= 1000 {
		metricItems = append(metricItems, map[string]any{"key": "untracked_ms", "label": "未标记", "value_ms": untracked, "value_text": duration(untracked), "important": untracked >= 10000})
	}
	if ms >= 60000 || len(metricItems) == 0 {
		metricItems = append(metricItems, map[string]any{"key": "total_ms", "label": "总耗时", "value_ms": ms, "value_text": duration(ms), "important": ms >= 10000})
	}
	reasonCode, reason := monitorSlowReason(metricItems)
	digest := []string{}
	ranked := append([]any{}, metricItems...)
	sort.SliceStable(ranked, func(i, j int) bool {
		return integer(object(ranked[i])["value_ms"]) > integer(object(ranked[j])["value_ms"])
	})
	for _, item := range ranked {
		m := object(item)
		if text(m["key"]) == "total_ms" {
			continue
		}
		digest = append(digest, text(m["label"])+" "+text(m["value_text"]))
		if len(digest) == 4 {
			break
		}
	}
	if len(digest) == 0 {
		digest = append(digest, "总耗时 "+duration(ms))
	}
	errorText := text(r.PublicError)
	if errorText == "" && r.ErrorCode != "" {
		errorText = "错误码：" + text(r.ErrorCode)
	}
	if tracked.persistFailed {
		errorText = "终态记录保存失败"
	}
	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	attemptText := fmt.Sprintf("%d 次尝试", max(r.Attempts, len(attempts)))
	if switches > 0 {
		attemptText += fmt.Sprintf(" · 切换 %d 次", switches)
	}
	view := map[string]any{"call_id": r.ID, "status": status, "endpoint": endpoint, "model": text(r.Model), "started_at": stamp(r.Started), "ended_at": stamp(r.Ended), "updated_at": stamp(end), "account_email": email, "account_id": accountID, "previous_account_email": previous, "stage": stage, "duration_ms": ms, "status_code": integer(detail["status_code"]), "error_code": text(r.ErrorCode), "upstream_request_id": text(detail["upstream_request_id"]), "timings_ms": timings, "metrics": safeMetrics(detail["metrics"]), "request_meta": safeRequestMeta(detail["request_meta"]), "request_shape": safeRequestShape(detail["request_shape"]), "attempts": attempts, "presentation": map[string]any{"status_label": present["label"], "status_tone": present["tone"], "stage_text": stageText, "error_text": errorText, "duration_text": duration(ms), "metric_digest": strings.Join(digest, " / "), "egress_text": egress, "account_attempt_text": attemptText, "account_egress_text": "账号 " + duration(integer(timings["account_wait_ms"])) + " / 出口 " + duration(integer(timings["egress_wait_ms"])), "tracked_duration_ms": trackedMS, "untracked_duration_ms": ms - trackedMS, "slow_metrics": metricItems, "slow_reason_code": reasonCode, "slow_reason": reason}}
	view["public_error"] = text(r.PublicError)
	view["switch_count"] = switches
	view["api_status_code"] = integer(r.APIStatusCode)
	if includeDetail {
		projected := []any{}
		for _, event := range businessEvents {
			projected = append(projected, projectMonitorEvent(object(event), r))
		}
		view["events"] = projected
		images, _ := safeResultImages(detail["result_images"], publicBaseURLs...)
		view["result_images"] = images
		// Logs and the live drawer consume the same detail projection. The live
		// payload contributes only the currently observed lifecycle values.
		detail["call_id"], detail["endpoint"], detail["model"], detail["status"], detail["stage"] = r.ID, endpoint, text(r.Model), status, stage
		detail["started_at"], detail["ended_at"], detail["duration_ms"] = stamp(r.Started), stamp(r.Ended), ms
		detail["account_id"], detail["account_email"], detail["conversation_id"] = accountID, email, text(r.ConversationID)
		detail["key_id"], detail["key_name"], detail["role"] = r.Subject.ID, r.Subject.Name, r.Subject.Role
		detail["error_code"], detail["public_error"], detail["timings_ms"] = text(r.ErrorCode), text(r.PublicError), coarse
		detail["api_status_code"] = view["api_status_code"]
		detail["upstream_error"], detail["upstream_text"], detail["raw_error"] = r.UpstreamError, r.UpstreamText, r.RawError
		detail["request_text_full"], detail["request_text_truncated"] = r.RequestText, r.RequestTextTruncated
		detail["image_requested_count"], detail["image_count"], detail["switch_count"] = r.RequestedCount, r.OutputCount, switches
		data, _ := json.Marshal(map[string]any{"id": r.ID, "type": "call", "time": stamp(r.Started), "detail": detail})
		if projected, err := projectCall(data, true, publicBaseURLs...); err == nil {
			view["detail_presentation"] = projected["detail_presentation"]
			for _, key := range []string{"request_text", "request_text_full", "request_text_truncated", "image_urls", "upstream_error", "upstream_text", "raw_error", "image_requested_count", "image_succeeded_count", "image_failed_count", "switch_count"} {
				view[key] = projected[key]
			}
		}
	} else {
		// 列表只展示摘要，原文仅通过管理员请求详情返回。
		delete(view, "attempts")
	}
	return view
}

// The default monitor is a business history. The bounded raw trace remains in
// diagnostics; transport callbacks and completed start markers add no stages.
func monitorBusinessEvents(tracked trackedCall, events []any, now time.Time) []any {
	result, starts := []any{}, []any{}
	measurements := mainMeasurements(events)
	for _, measurement := range measurements {
		result = append(result, measurement.event)
	}
	for _, raw := range append(append([]any{}, tracked.events...), events...) {
		event := object(raw)
		name := text(event["event"])
		switch name {
		case "request_started", "request_finished", "attempt_started", "account_switched":
			result = append(result, event)
			continue
		case "attempt_finished":
			if failedEvent(event) {
				result = append(result, event)
			}
			continue
		}
		if failedEvent(event) {
			covered := false
			at, _ := time.Parse(time.RFC3339Nano, text(event["time"]))
			for _, measurement := range measurements {
				if measurement.event == nil || !sameEventScope(event, measurement.event) || !failedEvent(measurement.event) {
					continue
				}
				if !at.Before(measurement.started) && !at.After(measurement.end) {
					covered = true
					break
				}
			}
			if !covered {
				failure := map[string]any{}
				for key, value := range event {
					failure[key] = value
				}
				if strings.HasPrefix(name, "http_") || strings.HasPrefix(name, "sse_") {
					failure["event"], failure["label"] = "upstream_transport_failed", "上游请求失败"
				}
				result = append(result, failure)
			}
			continue
		}
		if !tracked.record.Ended.IsZero() || !strings.HasSuffix(name, "_started") || stageTimingMetric(text(event["stage"])) == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, text(event["time"]))
		if err != nil {
			continue
		}
		finished := false
		for _, candidate := range events {
			endEvent := object(candidate)
			if text(endEvent["stage"]) != text(event["stage"]) || !sameEventScope(event, endEvent) || !strings.HasSuffix(text(endEvent["event"]), "_finished") {
				continue
			}
			start, end := eventInterval(endEvent)
			if !end.Before(at) && !start.After(at.Add(time.Millisecond)) {
				finished = true
				break
			}
		}
		if !finished {
			running := map[string]any{}
			for key, value := range event {
				running[key] = value
			}
			running["duration_ms"], running["metric"] = max(0, now.Sub(at).Milliseconds()), stageTimingMetric(text(event["stage"]))
			starts = append(starts, running)
		}
	}
	// An active parent such as requirements is more meaningful than its
	// prepare/finalize child. Then show the latest active stage per attempt.
	for _, raw := range starts {
		candidate, shadowed := object(raw), false
		for _, otherRaw := range starts {
			other := object(otherRaw)
			if !sameEventScope(candidate, other) {
				continue
			}
			if shadowsMetric(text(other["metric"]), text(candidate["metric"])) || text(other["time"]) > text(candidate["time"]) && !shadowsMetric(text(candidate["metric"]), text(other["metric"])) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			result = append(result, candidate)
		}
	}
	// Coarse lifecycle changes supply a current stage only when fine trace
	// instrumentation has no active stage to describe.
	if len(starts) == 0 && tracked.record.Ended.IsZero() && len(tracked.events) > 0 {
		latest := object(tracked.events[len(tracked.events)-1])
		if text(latest["event"]) == "stage_changed" {
			result = append(result, latest)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		left := monitorEventOrderTime(object(result[i]))
		right := monitorEventOrderTime(object(result[j]))
		if left.Equal(right) {
			priority := func(event map[string]any) int {
				switch text(event["event"]) {
				case "request_started":
					return 0
				case "attempt_started":
					return 1
				case "request_finished":
					return 3
				}
				return 2
			}
			return priority(object(result[i])) < priority(object(result[j]))
		}
		return left.Before(right)
	})
	deduplicated, seen := []any{}, map[string]bool{}
	for _, raw := range result {
		event := object(raw)
		key := fmt.Sprintf("%s/%s/%s/%d/%d", text(event["event"]), text(event["stage"]), text(event["time"]), integer(event["slot"]), integer(event["attempt"]))
		if !seen[key] {
			seen[key] = true
			deduplicated = append(deduplicated, event)
		}
	}
	return deduplicated
}

func monitorEventOrderTime(event map[string]any) time.Time {
	name := text(event["event"])
	if mainMetric(text(event["metric"])) && !strings.HasSuffix(name, "_started") && !strings.HasPrefix(name, "http_") && !strings.HasPrefix(name, "sse_") {
		start, _ := eventInterval(event)
		return start
	}
	at, _ := time.Parse(time.RFC3339Nano, text(event["time"]))
	return at
}

func projectMonitorEvent(event map[string]any, record callrecord.Record) map[string]any {
	result := map[string]any{}
	for key, value := range event {
		result[key] = value
	}
	result["time"] = displayTime(stamp(monitorEventOrderTime(event)))
	result["call_id"], result["model"] = record.ID, text(record.Model)
	label := first(event["label"], stageLabel(text(event["stage"])), stageLabel(text(event["event"])))
	if text(event["label"]) == "" {
		switch name := text(event["event"]); name {
		case "http_started":
			label = "HTTP 请求开始"
		case "http_headers_received":
			label = "收到 HTTP 响应头"
		case "http_finished":
			label = "HTTP 请求结束"
		case "http_trace_unavailable":
			label = "连接细节未采集"
		case "sse_first_event":
			label = "收到 SSE 首事件"
		case "sse_finished":
			label = "SSE 流结束"
		case "attempt_started":
			label = "尝试开始"
		case "attempt_finished":
			label = "尝试结束"
		default:
			if strings.HasSuffix(name, "_started") {
				label += "（进行中）"
			}
		}
	}
	result["label"] = label
	if integer(event["count"]) > 1 && (text(event["metric"]) == "poll_wait_ms" || text(event["metric"]) == "poll_request_ms") {
		result["label"] = map[string]string{"poll_wait_ms": "等待结果汇总", "poll_request_ms": "查询结果汇总"}[text(event["metric"])]
	}
	parts := []string{}
	if slot := integer(event["slot"]); slot > 0 {
		scope := ""
		if kind := callrecord.Business(record.Endpoint); kind == "image_generation" || kind == "image_edit" {
			scope = fmt.Sprintf("图片 %d", slot)
		}
		if attempt := integer(event["attempt"]); attempt > 0 {
			if scope != "" {
				scope += " · "
			}
			scope += fmt.Sprintf("第 %d 次尝试", attempt)
		}
		parts = append(parts, scope)
	}
	if event["switched_account"] == true {
		parts = append(parts, "已切换账号")
	}
	account := text(event["account_email"])
	if previous := text(event["previous_account_email"]); previous != "" && account != "" {
		account = previous + " → " + account
	}
	if account != "" {
		parts = append(parts, account)
	}
	if egress := safeEgress(event["egress"]); egress != "" {
		parts = append(parts, egress)
	}
	if code := text(event["error_code"]); code != "" {
		parts = append(parts, code)
	}
	if statusCode := integer(event["status_code"]); statusCode > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", statusCode))
	}
	if text(event["event"]) == "sse_finished" {
		parts = append(parts, fmt.Sprintf("%d 个 SSE 事件", integer(event["count"])))
	}
	if text(event["event"]) == "http_trace_unavailable" {
		parts = append(parts, fmt.Sprintf("%d 项 DNS/TCP/TLS 计时未采集；可能复用连接或传输层未提供回调，无法细分耗时", integer(event["count"])))
	}
	if len(parts) == 0 {
		parts = append(parts, stageLabel(text(event["stage"])))
	}
	result["detail_text"] = strings.Join(parts, " · ")
	ms := integer(event["duration_ms"])
	timingText := duration(ms)
	if metric := text(event["metric"]); metric != "" && ms > 0 {
		for _, spec := range timingSpecs {
			if spec.key == metric {
				timingText = spec.label + " " + duration(ms)
				break
			}
		}
	}
	if ms == 0 {
		for _, spec := range timingSpecs {
			if value := integer(event[spec.key]); value > 0 {
				timingText = spec.label + " " + duration(value)
				break
			}
		}
	}
	if count := integer(event["count"]); count > 1 && (text(event["metric"]) == "poll_wait_ms" || text(event["metric"]) == "poll_request_ms") {
		timingText = fmt.Sprintf("累计 %d 次 · 有效耗时 %s", count, duration(ms))
	} else if mainMetric(text(event["metric"])) && !strings.HasSuffix(text(event["event"]), "_started") && !strings.HasPrefix(text(event["event"]), "http_") && !strings.HasPrefix(text(event["event"]), "sse_") {
		start, end := eventInterval(event)
		if !start.IsZero() && !end.IsZero() {
			layout := "15:04:05.000"
			if start.In(beijing).Format("2006-01-02") != end.In(beijing).Format("2006-01-02") {
				layout = "2006-01-02 15:04:05.000"
			}
			timingText += " · " + start.In(beijing).Format(layout) + " → " + end.In(beijing).Format(layout)
		}
	}
	result["timing_text"] = timingText
	return result
}

func monitorSlowReason(items []any) (string, string) {
	var top map[string]any
	for _, item := range items {
		metric := object(item)
		key := text(metric["key"])
		if key == "total_ms" {
			continue
		}
		if top == nil || integer(metric["value_ms"]) > integer(top["value_ms"]) {
			top = metric
		}
	}
	if integer(top["value_ms"]) < 1000 {
		return "", ""
	}
	key, label, value := text(top["key"]), text(top["label"]), text(top["value_text"])
	switch key {
	case "untracked_ms":
		return "instrumentation_gap", "仍有 " + value + " 未落到具体阶段"
	case "poll_wait_ms":
		return "image_poll_wait", "主要卡在等待生成结果，可能是任务尚未完成或轮询退避"
	case "poll_request_ms":
		return "image_poll_request", "主要卡在查询图片结果"
	case "resolve_ms":
		return "image_result_resolve", "主要卡在解析图片结果"
	case "conversation_stream_ms", "upstream_ms", "stream_ms":
		return "upstream_generation", "主要卡在上游生成"
	case "stream_error_ms":
		return "upstream_stream_error", "主要卡在上游断流"
	case "http_wait_ms", "http_ttfb_ms":
		return "http_first_byte", "主要卡在 HTTP 首包，可能是代理出口、上游节点或请求排队变慢"
	case "http_dns_ms", "http_tcp_ms", "http_tls_ms":
		return "http_connect", "主要卡在 HTTP 建连：" + label + " " + value
	case "sse_first_event_ms":
		return "sse_first_event", "主要卡在 SSE 首事件"
	case "sse_max_gap_ms", "sse_last_gap_ms":
		return "sse_gap", "主要卡在 SSE 空窗，上游长时间没有返回新事件"
	case "account_wait_ms":
		return "account_wait", "主要卡在等待可用账号"
	case "egress_wait_ms", "egress_acquire_ms":
		return "egress_wait", "主要卡在准备代理出口"
	case "handler_queue_ms", "stream_first_queue_ms":
		return "entry_queue", "主要卡在等待入口"
	case "upload_ms", "bootstrap_ms", "requirements_ms", "prepare_conversation_ms", "generation_start_ms":
		return "upstream_prepare", "主要卡在上游准备：" + label + " " + value
	case "upscale_ms":
		return "image_upscale", "主要卡在超分图片"
	case "convert_ms":
		return "image_convert", "主要卡在转换图片"
	case "download_ms":
		return "image_download", "主要卡在下载图片"
	}
	return "primary_metric", "主要耗时：" + label + " " + value
}
