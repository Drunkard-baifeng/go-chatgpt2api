package observability

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
)

func capturedDiagnostic(values ...any) string {
	for _, value := range values {
		if raw, ok := value.(string); ok && strings.TrimSpace(raw) != "" {
			return callrecord.CaptureDiagnosticText(raw)
		}
	}
	return ""
}

func missingDiagnosticNotice(status string, upstreamError, upstreamText any) string {
	if (status == "failed" || status == "rate_limited" || status == "text_review") && upstreamError == "" && upstreamText == "" {
		return "这条记录未保留上游返回原文（历史记录无法补采）。"
	}
	return ""
}

func safeRequestMeta(value any) map[string]any {
	source, result := object(value), map[string]any{}
	for _, key := range []string{"size", "quality", "response_format", "output_format"} {
		if value := text(source[key]); value != "" {
			result[key] = value
		}
	}
	if source["n"] != nil {
		result["n"] = integer(source["n"])
	}
	if stream, ok := source["stream"].(bool); ok {
		result["stream"] = stream
	}
	return result
}

func safeRequestShape(value any) map[string]any {
	source, result := object(value), map[string]any{}
	for _, key := range []string{"messages", "images", "masks", "input_items", "text_parts", "image_parts", "input_image_parts", "image_url_parts", "data_url_images", "remote_image_urls", "literal_image_placeholders", "tools", "attachments", "reference_images"} {
		if source[key] != nil {
			result[key] = integer(source[key])
		}
	}
	return result
}

func localImageURL(value any, publicBaseURLs ...string) string {
	raw, _ := value.(string)
	raw = callrecord.ResultImageURL(raw)
	if strings.HasPrefix(raw, "/") && len(publicBaseURLs) > 0 && publicBaseURLs[0] != "" {
		return callrecord.ResultImageURL(strings.TrimRight(publicBaseURLs[0], "/") + raw)
	}
	return raw
}

func safeResultImages(value any, publicBaseURLs ...string) ([]any, []string) {
	images, resolutions := []any{}, []string{}
	for _, item := range array(value) {
		source := object(item)
		if len(source) == 0 {
			continue
		}
		width, height := integer(source["width"]), integer(source["height"])
		image := map[string]any{"width": width, "height": height, "bytes": integer(source["bytes"])}
		if path := localImageURL(source["url"], publicBaseURLs...); path != "" {
			image["url"] = path
		}
		images = append(images, image)
		resolution := "未知"
		if width > 0 && height > 0 {
			resolution = fmt.Sprintf("%d×%d", width, height)
		}
		resolutions = append(resolutions, resolution)
	}
	return images, resolutions
}

func safeMetrics(sources ...any) map[string]any {
	result := map[string]any{}
	for _, source := range sources {
		for _, spec := range timingSpecs {
			if value, ok := object(source)[spec.key]; ok {
				result[spec.key] = max(integer(result[spec.key]), integer(value))
			}
		}
		for _, key := range []string{"total_ms", "handler_exec_ms", "stream_ms", "http_total_ms"} {
			if value, ok := object(source)[key]; ok {
				result[key] = max(integer(result[key]), integer(value))
			}
		}
	}
	return result
}

func safeEgress(value any) string {
	raw, _ := value.(string)
	raw = strings.TrimSpace(raw)
	if raw == "direct" || raw == "直连" {
		return "直连"
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "代理出口"
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
			return u.Scheme + "://" + u.Host
		}
		return "代理出口"
	}
	return text(raw)
}

func safeEvents(value any) []any {
	result := []any{}
	for _, item := range array(value) {
		source := object(item)
		if len(source) == 0 {
			continue
		}
		event := map[string]any{}
		for _, key := range []string{"time", "started_at", "event", "stage", "status", "error_code", "upstream_request_id", "account_email", "previous_account_email", "public_error"} {
			if value := text(source[key]); value != "" {
				event[key] = value
			}
		}
		for _, key := range []string{"duration_ms", "status_code", "slot", "attempt", "count"} {
			if source[key] != nil {
				event[key] = integer(source[key])
			}
		}
		if egress := safeEgress(source["egress"]); egress != "" {
			event["egress"] = egress
		}
		if switched, ok := source["switched_account"].(bool); ok {
			event["switched_account"] = switched
		}
		metric := text(source["metric"])
		for _, spec := range timingSpecs {
			if metric == spec.key {
				event["metric"] = metric
			}
			if source[spec.key] != nil {
				event[spec.key] = integer(source[spec.key])
			}
		}
		result = append(result, event)
		if len(result) >= 128 {
			break
		}
	}
	return result
}

func displayTime(value any) string {
	raw := text(value)
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return at.In(beijing).Format("2006-01-02 15:04:05")
	}
	return raw
}

func safeMonitor(value any) map[string]any {
	source := object(value)
	result := map[string]any{"events": safeEvents(source["events"]), "metrics": safeMetrics(source["metrics"])}
	for _, key := range []string{"stage", "stage_label", "reason", "local_reason", "upstream_request_id"} {
		if value := text(source[key]); value != "" {
			result[key] = value
		}
	}
	if egress := safeEgress(source["egress"]); egress != "" {
		result["egress"] = egress
	}
	for _, key := range []string{"upstream_error", "upstream_text", "raw_error", "raw_upstream_error", "raw_upstream_message", "upstream_message", "upstream_message_preview", "upstream_preview"} {
		if value := capturedDiagnostic(source[key]); value != "" {
			result[key] = value
		}
	}
	return result
}

func projectAttempts(value any, events []any) []any {
	result := []any{}
	for _, item := range array(value) {
		source := object(item)
		if len(source) == 0 {
			continue
		}
		slot, number := max(int64(1), integer(source["slot"])), max(int64(1), integer(source["attempt"]))
		code := first(source["error_code"], source["failure_code"])
		status := text(source["status"])
		resultStatus := "failed"
		classified := outcome(status, code)
		if integer(source["status_code"]) == 429 {
			classified = "rate_limited"
		}
		if classified == "success" {
			resultStatus = "success"
		} else if text(source["failure_scope"]) == "delivery" || code == "image_download_failed" {
			resultStatus = "generated_but_delivery_failed"
		}
		monitor := safeMonitor(source["monitor"])
		attemptEvents := array(monitor["events"])
		for _, event := range events {
			if integer(object(event)["slot"]) == slot && integer(object(event)["attempt"]) == number {
				attemptEvents = append(attemptEvents, event)
			}
		}
		monitor["events"] = attemptEvents
		timings := safeMetrics(source["timings_ms"], source["metrics"], monitor["metrics"])
		ms := integer(source["duration_ms"])
		if ms == 0 {
			started, e1 := time.Parse(time.RFC3339Nano, text(source["started_at"]))
			ended, e2 := time.Parse(time.RFC3339Nano, text(source["ended_at"]))
			if e1 == nil && e2 == nil {
				ms = max(0, ended.Sub(started).Milliseconds())
			}
		}
		if ms == 0 {
			ms = max(integer(timings["total_ms"]), integer(timings["stream_ms"]), integer(timings["handler_exec_ms"]))
		}
		if ms > 0 {
			timings["total_ms"] = ms
		}
		attempt := map[string]any{"slot": slot, "attempt": number, "status": status, "outcome": classified, "result_status": resultStatus, "duration_ms": ms, "status_code": integer(source["status_code"]), "api_status_code": integer(source["api_status_code"]), "error_code": code, "error_label": code, "public_error": text(source["public_error"]), "upstream_error": capturedDiagnostic(source["upstream_error"], source["raw_upstream_error"]), "upstream_text": capturedDiagnostic(source["upstream_text"], source["raw_upstream_message"], source["upstream_message"], source["upstream_message_preview"]), "raw_error": capturedDiagnostic(source["raw_error"]), "switched_account": nil, "timings_ms": timings, "monitor": monitor}
		for _, key := range []string{"account_id", "account_email", "previous_account_email", "conversation_id", "started_at", "ended_at", "stage", "upstream_request_id"} {
			attempt[key] = text(source[key])
		}
		attempt["egress"] = safeEgress(source["egress"])
		if switched, ok := source["switched_account"].(bool); ok {
			attempt["switched_account"] = switched
		}
		present := statusPresentation(classified)
		failureLabel, marker, switchLabel := "生成失败", "danger", ""
		if code != "" {
			failureLabel = code
		}
		if resultStatus == "generated_but_delivery_failed" {
			present = map[string]any{"label": "生成成功", "tone": "warning"}
			failureLabel = "结果交付失败"
		}
		if resultStatus == "success" {
			marker = "success"
		}
		if attempt["switched_account"] == true {
			switchLabel = "切换账号"
		}
		statusParts := []string{}
		if n := integer(attempt["api_status_code"]); n > 0 {
			statusParts = append(statusParts, fmt.Sprintf("API HTTP %d", n))
		}
		if n := integer(attempt["status_code"]); n > 0 {
			statusParts = append(statusParts, fmt.Sprintf("上游 HTTP %d", n))
		}
		notice := missingDiagnosticNotice(classified, attempt["upstream_error"], attempt["upstream_text"])
		attempt["presentation"] = map[string]any{"status": present, "failure_label": failureLabel, "marker_tone": marker, "switch_label": switchLabel, "error_code_text": code, "status_code_text": strings.Join(statusParts, " · "), "show_failure": resultStatus != "success", "show_error_details": attempt["public_error"] != "" || attempt["upstream_error"] != "" || attempt["upstream_text"] != "" || attempt["raw_error"] != "" || notice != "", "diagnostic_notice": notice, "timeline": timeline(timings, attemptEvents)}
		result = append(result, attempt)
		if len(result) >= 32 {
			break
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := object(result[i]), object(result[j])
		if integer(a["slot"]) != integer(b["slot"]) {
			return integer(a["slot"]) < integer(b["slot"])
		}
		return integer(a["attempt"]) < integer(b["attempt"])
	})
	return result
}

func attemptGroups(attempts []any, requested int64) []any {
	grouped := map[int64][]any{}
	for _, item := range attempts {
		slot := integer(object(item)["slot"])
		grouped[slot] = append(grouped[slot], item)
		requested = max(requested, slot)
	}
	result := []any{}
	for slot := int64(1); slot <= min(requested, 32); slot++ {
		items, switches := grouped[slot], 0
		present := map[string]any{"label": "未记录", "tone": "muted"}
		if len(items) > 0 {
			present = statusPresentation("failed")
		}
		for _, item := range items {
			a := object(item)
			if a["switched_account"] == true {
				switches++
			}
			if a["result_status"] == "success" {
				present = statusPresentation("success")
			} else if a["result_status"] == "generated_but_delivery_failed" && present["tone"] != "success" {
				present = map[string]any{"label": "生成成功", "tone": "warning"}
			}
		}
		switchText := ""
		if switches > 0 {
			switchText = fmt.Sprintf("切换 %d 次", switches)
		}
		result = append(result, map[string]any{"slot": slot, "slot_label": fmt.Sprintf("图片 %d", slot), "attempt_count": len(items), "attempt_text": fmt.Sprintf("%d 次尝试", len(items)), "switch_count": switches, "switch_text": switchText, "status": present})
	}
	return result
}

func detailField(label string, value any, target *[]any) {
	var formatted string
	switch v := value.(type) {
	case string:
		formatted = text(v)
	case int:
		formatted = strconv.Itoa(v)
	case int64:
		formatted = strconv.FormatInt(v, 10)
	case bool:
		formatted = strconv.FormatBool(v)
	default:
		if len(object(value)) > 0 {
			data, _ := json.Marshal(value)
			formatted = string(data)
		}
	}
	if formatted != "" && formatted != "-" {
		*target = append(*target, map[string]any{"label": label, "value": formatted, "copyable": true, "wide": label == "时间" || label == "请求形状"})
	}
}
