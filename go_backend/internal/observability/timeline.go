package observability

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

var timingSpecs = []struct{ key, label, category string }{
	{"handler_queue_ms", "等待入口", "entry"}, {"stream_first_queue_ms", "读取首包", "entry"}, {"account_wait_ms", "等待账号", "entry"}, {"egress_wait_ms", "等待出口", "entry"}, {"egress_acquire_ms", "出口租约", "entry"},
	{"input_ms", "准备参考图", "prepare"}, {"upload_ms", "上传输入图", "prepare"}, {"bootstrap_ms", "预热页面", "prepare"}, {"requirements_ms", "获取请求令牌", "prepare"}, {"prepare_conversation_ms", "准备会话", "prepare"},
	{"http_dns_ms", "HTTP DNS", "prepare"}, {"http_tcp_ms", "HTTP TCP", "prepare"}, {"http_tls_ms", "HTTP TLS", "prepare"}, {"http_wait_ms", "HTTP 等待", "prepare"}, {"http_ttfb_ms", "HTTP 首包", "prepare"},
	{"generation_start_ms", "启动生成", "upstream"}, {"sse_stream_ms", "SSE 流耗时", "upstream"}, {"sse_first_event_ms", "SSE 首事件", "upstream"}, {"sse_max_gap_ms", "SSE 最大空窗", "upstream"}, {"sse_last_gap_ms", "SSE 收尾空窗", "upstream"},
	{"upstream_ms", "上游执行", "upstream"}, {"conversation_stream_ms", "上游生成", "upstream"}, {"stream_error_ms", "上游断流", "upstream"}, {"poll_wait_ms", "等待结果", "resolve"}, {"poll_request_ms", "查询结果", "resolve"}, {"resolve_ms", "解析结果", "resolve"}, {"response_ms", "响应整理", "resolve"}, {"download_ms", "下载图片", "download"}, {"publish_ms", "保存图片", "resolve"}, {"delivery_ms", "交付结果", "resolve"},
	{"processing_ms", "处理图片", "resolve"}, {"convert_ms", "转换图片", "resolve"}, {"upscale_ms", "超分图片", "resolve"},
	{"cleanup_ms", "删除官网图片会话", "resolve"},
	{"stream_ms", "上游生成", "upstream"}, {"http_total_ms", "HTTP 总耗时", "prepare"},
	{"upload_prepare_ms", "准备上传输入图", "prepare"}, {"upload_complete_ms", "确认上传输入图", "prepare"}, {"requirements_prepare_ms", "准备请求令牌", "prepare"}, {"requirements_finalize_ms", "完成请求令牌", "prepare"},
}

func metricTone(key string, ms int64) string {
	if key == "stream_error_ms" {
		return "danger"
	}
	threshold := int64(60000)
	switch key {
	case "handler_queue_ms", "stream_first_queue_ms", "http_dns_ms":
		threshold = 1000
	case "http_tcp_ms":
		threshold = 3000
	case "http_tls_ms":
		threshold = 5000
	case "account_wait_ms", "egress_wait_ms", "egress_acquire_ms":
		threshold = 10000
	case "http_wait_ms", "http_ttfb_ms", "sse_first_event_ms", "sse_last_gap_ms", "poll_request_ms", "response_ms":
		threshold = 30000
	}
	if ms >= threshold {
		return "warning"
	}
	return "info"
}

func mainMetric(key string) bool {
	return key != "" && !strings.HasPrefix(key, "http_") && !strings.HasPrefix(key, "sse_")
}

func stageTimingMetric(stage string) string {
	if metric := stageMetric(stage); metric != "" {
		return metric
	}
	if metric := map[string]string{"account_selected": "account_wait_ms", "egress": "egress_acquire_ms", "upload": "upload_ms", "image_upload_prepare": "upload_prepare_ms", "editable_upload_prepare": "upload_prepare_ms", "image_upload_complete": "upload_complete_ms", "editable_upload_complete": "upload_complete_ms", "image_stream": "stream_ms", "text_stream": "stream_ms", "search_stream": "stream_ms", "editable_stream": "stream_ms", "image_poll_wait": "poll_wait_ms", "editable_poll_wait": "poll_wait_ms", "image_poll_request": "poll_request_ms", "image_resolve": "resolve_ms", "image_download": "download_ms", "download": "download_ms", "image_convert": "convert_ms", "image_upscale": "upscale_ms", "delivery": "response_ms"}[stage]; metric != "" {
		return metric
	}
	for _, spec := range timingSpecs {
		if stage+"_ms" == spec.key && mainMetric(spec.key) {
			return spec.key
		}
	}
	return ""
}

// These relationships describe nested work, not arithmetic identities. Keep
// requirements/upload as a whole, and processing/upstream at the useful child
// level when measured children exist in the same image attempt and interval.
func shadowsMetric(kept, other string) bool {
	switch kept {
	case "requirements_ms":
		return other == "requirements_prepare_ms" || other == "requirements_finalize_ms"
	case "upload_ms":
		return other == "upload_prepare_ms" || other == "upload_complete_ms"
	case "convert_ms", "upscale_ms":
		return other == "processing_ms"
	case "stream_ms":
		return other == "conversation_stream_ms" || other == "stream_error_ms"
	case "response_ms":
		return other == "delivery_ms"
	}
	if other == "upstream_ms" {
		switch kept {
		case "upload_ms", "bootstrap_ms", "requirements_ms", "prepare_conversation_ms", "generation_start_ms", "stream_ms", "conversation_stream_ms", "stream_error_ms", "poll_wait_ms", "poll_request_ms", "resolve_ms", "download_ms":
			return true
		}
	}
	return false
}

func selectedMetricKeys(timings map[string]any) map[string]bool {
	keys := map[string]bool{}
	for _, spec := range timingSpecs {
		if mainMetric(spec.key) && integer(timings[spec.key]) > 0 {
			keys[spec.key] = true
		}
	}
	for key := range keys {
		for other := range keys {
			if shadowsMetric(key, other) {
				delete(keys, other)
			}
		}
	}
	return keys
}

type stageMeasurement struct {
	event        map[string]any
	metric       string
	started, end time.Time
	ms           int64
	incomplete   bool
}

func failedEvent(event map[string]any) bool {
	status := text(event["status"])
	return status == "failed" || status == "cancelled" || status == "canceled" || (text(event["error_code"]) != "" && status != "unavailable")
}

func sameEventScope(a, b map[string]any) bool {
	return integer(a["slot"]) == integer(b["slot"]) && integer(a["attempt"]) == integer(b["attempt"])
}

func eventInterval(event map[string]any) (time.Time, time.Time) {
	end, endErr := time.Parse(time.RFC3339Nano, text(event["time"]))
	if endErr != nil {
		return time.Time{}, time.Time{}
	}
	start, err := time.Parse(time.RFC3339Nano, text(event["started_at"]))
	if err != nil || start.After(end) {
		ms := min(integer(event["duration_ms"]), int64((1<<63-1)/int64(time.Millisecond)))
		start = end.Add(-time.Duration(ms) * time.Millisecond)
	}
	return start, end
}

func mainMeasurements(events []any) []stageMeasurement {
	measured, result := []stageMeasurement{}, []stageMeasurement{}
	seen := map[string]bool{}
	for _, raw := range events {
		event := object(raw)
		name, metric := text(event["event"]), text(event["metric"])
		if strings.HasPrefix(name, "http_") || strings.HasPrefix(name, "sse_") || strings.HasSuffix(name, "_started") || !mainMetric(metric) {
			continue
		}
		ms := integer(event["duration_ms"])
		if ms == 0 && !failedEvent(event) {
			continue
		}
		start, end := eventInterval(event)
		identity := fmt.Sprintf("%s/%d/%d/%s/%s", metric, integer(event["slot"]), integer(event["attempt"]), stamp(start), stamp(end))
		if !end.IsZero() && seen[identity] {
			continue
		}
		seen[identity] = true
		measured = append(measured, stageMeasurement{event: event, metric: metric, started: start, end: end, ms: ms})
	}
	for index, candidate := range measured {
		shadowed := false
		for otherIndex, other := range measured {
			if index == otherIndex || !sameEventScope(candidate.event, other.event) || !shadowsMetric(other.metric, candidate.metric) {
				continue
			}
			// Rounding old durations to milliseconds can move the recovered
			// boundary slightly. New spans carry the precise start timestamp.
			if other.end.After(candidate.started) && candidate.end.After(other.started) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			result = append(result, candidate)
		}
	}
	// 兼容旧日志的逐轮事件；每张图片、每次尝试分别汇总成功轮询，失败独立保留。
	compact := []stageMeasurement{}
	polls := map[string]int{}
	for _, measurement := range result {
		if (measurement.metric != "poll_wait_ms" && measurement.metric != "poll_request_ms") || failedEvent(measurement.event) || measurement.started.IsZero() {
			compact = append(compact, measurement)
			continue
		}
		key := fmt.Sprintf("%s/%d/%d", measurement.metric, integer(measurement.event["slot"]), integer(measurement.event["attempt"]))
		if index, ok := polls[key]; ok {
			previous := &compact[index]
			previous.ms += measurement.ms
			previous.event["count"] = max(int64(1), integer(previous.event["count"])) + max(int64(1), integer(measurement.event["count"]))
			if measurement.started.Before(previous.started) {
				previous.started = measurement.started
			}
			if measurement.end.After(previous.end) {
				previous.end = measurement.end
			}
			previous.event["started_at"], previous.event["time"], previous.event["duration_ms"] = stamp(previous.started), stamp(previous.end), previous.ms
		} else {
			cloned := map[string]any{}
			for key, value := range measurement.event {
				cloned[key] = value
			}
			measurement.event = cloned
			polls[key] = len(compact)
			compact = append(compact, measurement)
		}
	}
	result = compact
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].started.IsZero() != result[j].started.IsZero() {
			return !result[i].started.IsZero()
		}
		if result[i].started.Equal(result[j].started) {
			return result[i].end.Before(result[j].end)
		}
		return result[i].started.Before(result[j].started)
	})
	return result
}

// Overview segments come only from the lifecycle owner's disjoint wall-clock
// buckets. Fine measurements may overlap across slots and never form a sum.
func timeline(timings map[string]any, events []any, coarseSources ...map[string]any) map[string]any {
	segments, groups, legend, steps, legacy := []any{}, []any{}, []any{}, []any{}, []any{}
	coarse := map[string]any{}
	if len(coarseSources) > 0 {
		coarse = coarseSources[0]
	}
	for _, key := range []string{"handler_queue_ms", "stream_first_queue_ms", "account_wait_ms", "input_ms", "upstream_ms", "processing_ms", "publish_ms", "delivery_ms"} {
		for _, spec := range timingSpecs {
			if spec.key != key || integer(coarse[key]) <= 0 {
				continue
			}
			ms := integer(coarse[key])
			segments = append(segments, map[string]any{"key": key, "label": spec.label, "category": spec.category, "value_ms": ms, "value_text": duration(ms), "tone": metricTone(key, ms)})
		}
	}
	measured := mainMeasurements(events)
	// 旧窗口可能只剩后几轮；单次尝试的累计指标仍完整。补齐有效耗时，
	// 但不补造丢失的轮询次数和起止时间；多图或多次尝试不猜测归属。
	for _, metric := range []string{"poll_wait_ms", "poll_request_ms"} {
		index, total, singleScope := -1, int64(0), true
		var scope map[string]any
		for i, measurement := range measured {
			if measurement.metric != metric {
				continue
			}
			total += measurement.ms
			if scope != nil && !sameEventScope(scope, measurement.event) {
				singleScope = false
			}
			scope = measurement.event
			if !failedEvent(measurement.event) {
				index = i
			}
		}
		if missing := integer(timings[metric]) - total; singleScope && index >= 0 && missing > 0 {
			measured[index].ms += missing
			// 单轮毫秒向下取整会产生不足一毫秒的差值，不能据此声称历史丢失。
			measured[index].incomplete = missing > max(int64(1), integer(measured[index].event["count"]))
		}
	}
	measuredKeys := map[string]bool{}
	for index, measurement := range measured {
		measuredKeys[measurement.metric] = true
		for _, spec := range timingSpecs {
			if spec.key != measurement.metric {
				continue
			}
			tone, label := metricTone(spec.key, measurement.ms), "记录"
			if failedEvent(measurement.event) {
				tone, label = "danger", "失败"
			} else if tone == "warning" {
				label = "慢"
			}
			step := map[string]any{"key": fmt.Sprintf("%s-%d", spec.key, index), "metric": spec.key, "label": spec.label, "category": spec.category, "value_ms": measurement.ms, "value_text": duration(measurement.ms), "tone": tone, "status_label": label, "time": "", "started_at": stamp(measurement.started), "ended_at": stamp(measurement.end), "slot": integer(measurement.event["slot"]), "attempt": integer(measurement.event["attempt"]), "description": ""}
			if measurement.started.IsZero() {
				step["description"] = "未采集独立起止时间"
				legacy = append(legacy, step)
			} else {
				step["time"] = measurement.started.In(beijing).Format("15:04:05") + " → " + measurement.end.In(beijing).Format("15:04:05")
				if measurement.started.In(beijing).Format("2006-01-02") != measurement.end.In(beijing).Format("2006-01-02") {
					step["time"] = displayTime(stamp(measurement.started)) + " → " + displayTime(stamp(measurement.end))
				}
				if count := integer(measurement.event["count"]); count > 1 {
					step["status_label"] = "累计"
					step["time"] = ""
					step["description"] = fmt.Sprintf("累计 %d 次 · 非连续执行，按各次耗时累计", count)
				}
				if measurement.incomplete {
					step["status_label"], step["time"] = "累计", ""
					step["description"] = "采用完整累计耗时；部分早期轮询事件未保留，次数与区间不完整"
				}
				steps = append(steps, step)
			}
		}
	}
	// Legacy records sometimes contain only cumulative metrics. Keep these
	// separate, with no invented event time or contribution to the overview.
	mainKeys := selectedMetricKeys(timings)
	for _, spec := range timingSpecs {
		if !mainKeys[spec.key] || measuredKeys[spec.key] {
			continue
		}
		suppressed := false
		for measuredKey := range measuredKeys {
			if shadowsMetric(measuredKey, spec.key) || shadowsMetric(spec.key, measuredKey) {
				suppressed = true
			}
		}
		if suppressed {
			continue
		}
		ms := integer(timings[spec.key])
		legacy = append(legacy, map[string]any{"key": spec.key, "metric": spec.key, "label": spec.label, "category": spec.category, "value_ms": ms, "value_text": duration(ms), "tone": metricTone(spec.key, ms), "status_label": "累计", "time": "", "description": "阶段累计；未采集独立起止时间"})
	}
	if len(steps) > 0 {
		groups = append(groups, map[string]any{"key": "execution", "label": "关键阶段", "steps": steps})
	}
	if len(legacy) > 0 {
		groups = append(groups, map[string]any{"key": "unpositioned", "label": "无独立时间记录", "steps": legacy})
	}
	for _, category := range []struct{ key, label string }{{"entry", "入口与账号"}, {"prepare", "上游准备"}, {"upstream", "上游生成"}, {"resolve", "结果处理"}, {"download", "图片下载"}} {
		for _, segment := range segments {
			if object(segment)["category"] == category.key {
				legend = append(legend, map[string]any{"key": category.key, "label": category.label, "category": category.key, "tone": "info"})
				break
			}
		}
	}
	return map[string]any{"segments": segments, "legend_items": legend, "groups": groups, "description": "总览为请求总耗时；重复轮询按阶段汇总，并发步骤不可相加。"}
}
