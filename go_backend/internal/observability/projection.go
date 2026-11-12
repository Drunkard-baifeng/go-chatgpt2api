package observability

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"chatgpt2api/go_backend/internal/callrecord"
)

var diagnosticURL = regexp.MustCompile(`(?i)(?:https?|socks5h?)://[^\s<>"']+`)

func text(value any) string {
	raw, _ := value.(string)
	raw = strings.TrimSpace(raw)
	raw = callrecord.SafeDiagnosticText(raw)
	raw = diagnosticURL.ReplaceAllString(raw, "[地址已隐藏]")
	runes := []rune(raw)
	if len(runes) > 2000 {
		raw = string(runes[:2000]) + "…"
	}
	return raw
}
func integer(value any) int64 {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v < float64(math.MaxInt64) {
			n = int64(v)
		}
	case json.Number:
		n, _ = v.Int64()
	case string:
		n, _ = strconv.ParseInt(v, 10, 64)
	}
	return max(0, n)
}
func object(value any) map[string]any {
	m, _ := value.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func array(value any) []any {
	a, _ := value.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func first(values ...any) string {
	for _, value := range values {
		if t := text(value); t != "" {
			return t
		}
	}
	return ""
}
func duration(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	if ms < 60000 {
		return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + " s"
	}
	return strconv.FormatFloat(float64(ms)/60000, 'f', 2, 64) + " min"
}
func outcome(status, code string) string {
	switch status {
	case "success", "partial_success", "text_review", "rate_limited", "running":
		return status
	}
	if strings.Contains(code, "rate_limit") || strings.Contains(code, "quota_exhausted") {
		return "rate_limited"
	}
	if code != "" {
		return "failed"
	}
	if status == "failed" || status == "cancelled" || status == "canceled" || status == "error" {
		return "failed"
	}
	return "unknown"
}
func statusPresentation(status string) map[string]any {
	label, tone := "记录", "muted"
	switch status {
	case "success":
		label, tone = "成功", "success"
	case "partial_success":
		label, tone = "部分成功", "warning"
	case "failed":
		label, tone = "失败", "danger"
	case "rate_limited":
		label, tone = "限流", "warning"
	case "text_review":
		label, tone = "文本", "info"
	case "running":
		label, tone = "进行中", "info"
	}
	return map[string]any{"label": label, "tone": tone}
}
func stageLabel(stage string) string {
	switch stage {
	case "image_poll_tasks_request":
		return "查询图片任务"
	case "image_settle_wait":
		return "图片二次确认等待"
	case "image_cleanup":
		return "删除官网图片会话"
	case "conversation_stream":
		return "上游生成"
	case "egress_acquire":
		return "获取图片出口"
	}
	if label := map[string]string{"editable_generation": "上游文件生成", "editable_poll": "查询文件结果", "editable_poll_wait": "等待文件结果", "file_publish": "保存文件", "http_request": "HTTP 请求", "http_connection_details": "HTTP 连接诊断", "http_dns": "HTTP DNS", "http_tcp": "HTTP TCP", "http_tls": "HTTP TLS", "http_wait": "HTTP 等待", "http_ttfb": "HTTP 首包", "image_stream": "上游生图流", "text_stream": "上游文本流", "search_stream": "搜索结果流", "editable_stream": "文件生成流", "image_poll_wait": "等待图片结果", "image_poll_request": "查询图片结果", "image_resolve": "解析图片结果", "image_download": "下载图片", "image_convert": "转换图片", "image_upscale": "超分图片", "requirements_prepare": "准备请求令牌", "requirements_finalize": "完成请求令牌", "image_upload_prepare": "准备上传输入图", "editable_upload_prepare": "准备上传输入图", "image_upload_complete": "确认上传输入图", "editable_upload_complete": "确认上传输入图"}[stage]; label != "" {
		return label
	}
	if label := map[string]string{"account_selection": "选择账号", "account_selected": "账号就绪", "upstream": "上游执行", "bootstrap": "准备上游", "conversation": "上游会话", "delivery": "交付结果", "download": "下载图片", "poll": "查询图片", "poll_wait": "等待图片结果", "resolve": "解析结果", "requirements": "获取请求令牌", "prepare_conversation": "准备会话", "generation_start": "启动生成", "egress": "准备出口", "upload": "上传参考图", "completed": "已完成", "request_started": "请求开始", "request_finished": "请求结束", "image_inputs": "准备参考图", "image_upstream": "上游生图", "image_processing": "处理图片", "convert": "转换图片", "upscale": "超分图片", "image_publish": "保存图片"}[stage]; label != "" {
		return label
	}
	return text(stage)
}

func projectCall(raw []byte, includeDetail bool, publicBaseURLs ...string) (map[string]any, error) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return nil, callrecord.ErrStorage
	}
	detail := object(root["detail"])
	if len(detail) == 0 {
		detail = root
	}
	value := func(key string) any {
		if detail[key] != nil && detail[key] != "" {
			return detail[key]
		}
		switch key {
		case "reason", "local_reason", "upstream_error_type", "error_type", "upstream_request_id", "stage":
			if value := object(detail["diagnosis"])[key]; value != nil {
				return value
			}
		}
		return root[key]
	}
	endpoint := text(value("endpoint"))
	business := callrecord.Business(endpoint)
	if text(root["type"]) == "account" {
		business = "account"
	}
	code := first(value("error_code"), value("failure_code"))
	result := outcome(first(value("outcome"), value("status")), code)
	publicError := first(value("public_error"), value("error"))
	if publicError == "" && code != "" {
		publicError = "调用未完成，请查看错误码与阶段"
	}
	ms := integer(value("duration_ms"))
	meta := safeRequestMeta(value("request_meta"))
	shape := safeRequestShape(value("request_shape"))
	images, resolutions := safeResultImages(value("result_images"), publicBaseURLs...)
	monitor := safeMonitor(value("monitor"))
	attemptValues := value("attempts")
	if len(array(attemptValues)) == 0 {
		attemptValues = value("image_attempts")
	}
	attempts := projectAttempts(attemptValues, array(monitor["events"]))
	switches := integer(value("switch_count"))
	var derivedSwitches int64
	for _, attempt := range attempts {
		if object(attempt)["switched_account"] == true {
			derivedSwitches++
		}
	}
	switches = max(switches, derivedSwitches)
	requested := integer(value("image_requested_count"))
	if business == "image_generation" || business == "image_edit" {
		requested = max(requested, integer(meta["n"]))
		for _, attempt := range attempts {
			requested = max(requested, integer(object(attempt)["slot"]))
		}
	}
	succeeded := integer(value("image_succeeded_count"))
	if succeeded == 0 {
		succeeded = integer(value("image_count"))
	}
	if succeeded == 0 && (business == "image_generation" || business == "image_edit") {
		succeeded = int64(len(images))
	}
	failed := integer(value("image_failed_count"))
	files := int64(0)
	if business == "file" {
		files = integer(value("file_count"))
		if files == 0 {
			// 早期文件调用将产物数写入 image_count，按真实端点纠正其单位。
			files = integer(value("image_count"))
		}
		requested, succeeded, failed = 0, 0, 0
	}
	if requested > 0 && succeeded < requested && failed == 0 {
		failed = requested - succeeded
	}
	urls := []any{}
	for _, url := range array(value("image_urls")) {
		v := localImageURL(url, publicBaseURLs...)
		if business != "file" && v != "" {
			urls = append(urls, v)
		}
	}
	if business != "file" && len(urls) == 0 {
		for _, image := range images {
			if path := localImageURL(object(image)["url"]); path != "" {
				urls = append(urls, path)
			}
		}
	}
	preview := ""
	if len(urls) > 0 {
		preview, _ = urls[0].(string)
	}
	label := map[string]string{"chat": "对话", "responses": "响应", "messages": "消息", "search": "搜索", "image_generation": "文生图", "image_edit": "图生图", "file": "文件", "account": "账号操作", "other": "其他"}[business]
	status := statusPresentation(result)
	resultText := publicError
	if resultText == "" {
		resultText = map[string]string{"success": "调用完成", "running": "正在执行", "partial_success": "部分图片已生成", "text_review": "上游返回文本", "rate_limited": "请求受限", "failed": "调用失败"}[result]
	}
	if resultText == "" {
		resultText = "调用记录"
	}
	if succeeded > 0 {
		resultText = fmt.Sprintf("生成 %d 张图片", succeeded)
		if requested > succeeded {
			resultText = fmt.Sprintf("生成 %d/%d 张图片", succeeded, requested)
		}
	}
	if files > 0 {
		resultText = fmt.Sprintf("生成 %d 个文件", files)
	}
	summary := text(root["summary"])
	if summary == "" {
		summary = resultText
	}
	durationTone := "success"
	if result == "failed" {
		durationTone = "danger"
	} else if ms >= 60000 {
		durationTone = "warning"
	}
	caller := first(value("key_name"), value("key_id"))
	if caller == "" {
		caller = "-"
	}
	view := map[string]any{"id": first(root["id"], value("call_id")), "time": text(root["time"]), "type": first(root["type"], "call"), "summary": summary, "business": business, "outcome": result, "display_status": status["label"], "endpoint": endpoint, "model": text(value("model")), "started_at": text(value("started_at")), "ended_at": text(value("ended_at")), "duration_ms": ms, "key_id": text(value("key_id")), "key_name": text(value("key_name")), "role": text(value("role")), "account_email": text(value("account_email")), "conversation_id": text(value("conversation_id")), "status_code": integer(value("status_code")), "api_status_code": integer(value("api_status_code")), "error_code": code, "public_error": publicError, "image_requested_count": requested, "image_succeeded_count": succeeded, "image_failed_count": failed, "image_result_status": text(value("image_result_status")), "preview_image_url": preview, "attempt_count": integer(value("attempt_count")), "switch_count": integer(value("switch_count")), "recovered_after_switch": false}
	if business == "file" {
		view["file_count"] = files
	}
	view["attempt_count"] = max(integer(view["attempt_count"]), int64(len(attempts)))
	view["switch_count"] = switches
	view["recovered_after_switch"] = switches > 0 && (result == "success" || result == "partial_success")
	diagnosticParts := []string{}
	if statusCode := integer(view["api_status_code"]); statusCode > 0 {
		diagnosticParts = append(diagnosticParts, fmt.Sprintf("API HTTP %d", statusCode))
	}
	if statusCode := integer(view["status_code"]); statusCode > 0 {
		diagnosticParts = append(diagnosticParts, fmt.Sprintf("上游 HTTP %d", statusCode))
	}
	if code != "" {
		diagnosticParts = append(diagnosticParts, code)
	}
	diagnostics := strings.Join(diagnosticParts, " · ")
	view["presentation"] = map[string]any{"request": map[string]any{"kind": label, "primary": first(view["model"], "调用日志"), "secondary": endpoint, "parameters": ""}, "execution": map[string]any{"primary": fmt.Sprintf("%d 次尝试", integer(view["attempt_count"])), "secondary": ""}, "key": map[string]any{"primary": caller, "secondary": ""}, "status": status, "result": map[string]any{"text": resultText, "diagnostics": diagnostics, "resolution": ""}, "summary_text": summary, "duration": map[string]any{"text": duration(ms), "breakdown": "", "tone": durationTone}, "is_failure": result == "failed"}
	presentation := object(view["presentation"])
	parameters := []string{}
	for _, spec := range []struct{ key, label string }{{"size", "请求"}, {"quality", "质量"}, {"response_format", "返回"}, {"output_format", "输出"}} {
		if value := text(meta[spec.key]); value != "" {
			parameters = append(parameters, spec.label+" "+value)
		}
	}
	object(presentation["request"])["parameters"] = strings.Join(parameters, " · ")
	uniqueResolutions := []string{}
	for _, resolution := range resolutions {
		found := false
		for _, item := range uniqueResolutions {
			if item == resolution {
				found = true
				break
			}
		}
		if !found {
			uniqueResolutions = append(uniqueResolutions, resolution)
		}
	}
	object(presentation["result"])["resolution"] = strings.Join(uniqueResolutions, " / ")
	if requested > 0 {
		object(presentation["execution"])["primary"] = fmt.Sprintf("%d 个任务 · %d 次尝试", requested, integer(view["attempt_count"]))
	}
	durationsBySlot := map[int64][]string{}
	for _, attempt := range attempts {
		a := object(attempt)
		if n := integer(a["duration_ms"]); n > 0 {
			slot := integer(a["slot"])
			durationsBySlot[slot] = append(durationsBySlot[slot], duration(n))
		}
	}
	slots := []int64{}
	for slot := range durationsBySlot {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	breakdown := []string{}
	for _, slot := range slots {
		if items := durationsBySlot[slot]; len(items) > 1 {
			expression := strings.Join(items, " + ")
			if len(slots) > 1 {
				expression = fmt.Sprintf("图 %d：%s", slot, expression)
			}
			breakdown = append(breakdown, expression)
		}
	}
	if len(breakdown) > 0 {
		object(presentation["duration"])["breakdown"] = "(" + strings.Join(breakdown, "；") + ")"
	}
	if !includeDetail {
		return view, nil
	}
	// 输入文字单独有界脱敏；其它 detail 只接收已声明的安全字段。
	safe := map[string]any{}
	for _, key := range []string{"call_id", "endpoint", "model", "upstream_model", "resolved_model", "thinking_effort", "account_id", "account_email", "conversation_id", "message_id", "stage", "failure_code", "error_code", "started_at", "ended_at", "status", "key_id", "key_name", "role", "public_error", "reason", "local_reason", "upstream_error_type", "error_type", "upstream_request_id"} {
		if v := text(value(key)); v != "" {
			safe[key] = v
		}
	}
	safe["duration_ms"] = ms
	if business == "file" {
		safe["file_count"] = files
	}
	timings := safeMetrics(value("timings_ms"), value("metrics"), value("perf"), monitor["metrics"])
	for key, value := range safeMetrics(value("metrics"), monitor["metrics"]) {
		timings[key] = value
	}
	if ms > 0 {
		timings["total_ms"] = ms
	}
	view["upstream_error"] = capturedDiagnostic(value("upstream_error"), value("raw_upstream_error"), monitor["upstream_error"], monitor["raw_upstream_error"])
	view["upstream_text"] = capturedDiagnostic(value("upstream_text"), value("raw_upstream_message"), value("upstream_message"), value("upstream_message_preview"), value("upstream_preview"), monitor["upstream_text"], monitor["raw_upstream_message"], monitor["upstream_message"], monitor["upstream_message_preview"], monitor["upstream_preview"])
	view["raw_error"] = capturedDiagnostic(value("raw_error"), monitor["raw_error"])
	for _, key := range []string{"upstream_error", "upstream_text", "raw_error", "api_status_code"} {
		safe[key] = view[key]
	}
	input, _ := value("request_text_full").(string)
	if input == "" {
		input, _ = value("request_text").(string)
	}
	input, inputTruncated := callrecord.CaptureRequestText(input)
	excerpt := strings.Join(strings.Fields(input), " ")
	if runes := []rune(excerpt); len(runes) > 1000 {
		excerpt = string(runes[:999]) + "…"
	}
	view["request_text_full"], view["request_text"] = input, excerpt
	view["request_text_truncated"] = inputTruncated || value("request_text_truncated") == true
	safe["request_text_full"], safe["request_text_truncated"] = input, view["request_text_truncated"]
	view["request_shape"] = shape
	view["request_meta"] = meta
	view["result_images"] = images
	view["image_urls"] = urls
	view["attempts"] = attempts
	view["timings_ms"] = timings
	view["perf"] = safeMetrics(value("perf"))
	view["metrics"] = safeMetrics(value("metrics"))
	view["monitor"] = monitor
	safe["request_meta"], safe["request_shape"], safe["result_images"], safe["attempts"], safe["timings_ms"], safe["metrics"], safe["monitor"], safe["status_code"] = meta, shape, images, attempts, timings, view["metrics"], monitor, view["status_code"]
	egress := safeEgress(value("egress"))
	if egress != "" {
		safe["egress"] = egress
	}
	view["raw_detail"] = safe
	primary := []any{}
	diagnostic := []any{}
	field := detailField
	started := displayTime(first(view["started_at"], view["time"]))
	timeRange := started
	if ended := displayTime(view["ended_at"]); ended != "" && ended != started {
		timeRange += " → " + ended
	}
	for _, item := range []struct {
		label string
		value any
	}{{"请求 ID", view["id"]}, {"接口", endpoint}, {"模型", view["model"]}, {"请求尺寸", meta["size"]}, {"实际分辨率", strings.Join(resolutions, " · ")}, {"请求质量", meta["quality"]}, {"返回格式", meta["response_format"]}, {"输出格式", meta["output_format"]}, {"请求张数", meta["n"]}, {"密钥", first(value("key_name"), value("key_id"))}, {"出口", strings.Replace(egress, "://", " · ", 1)}, {"时间", timeRange}} {
		field(item.label, item.value, &primary)
	}
	if len(resolutions) > 0 {
		field("出图张数", len(resolutions), &primary)
	}
	isImage := business == "image_generation" || business == "image_edit"
	if !isImage || len(attempts) <= 1 && requested <= 1 {
		field("账号", first(value("account_email"), value("account_id")), &primary)
		field("会话 ID", view["conversation_id"], &primary)
	}
	if statusCode := integer(view["api_status_code"]); statusCode > 0 {
		field("API HTTP", statusCode, &diagnostic)
	}
	if statusCode := integer(view["status_code"]); statusCode > 0 {
		field("上游 HTTP", statusCode, &diagnostic)
	}
	field("错误码", code, &diagnostic)
	field("阶段", stageLabel(text(value("stage"))), &diagnostic)
	field("原因", first(value("reason"), value("local_reason")), &diagnostic)
	field("上游错误", first(value("upstream_error_type"), value("error_type")), &diagnostic)
	field("上游请求 ID", value("upstream_request_id"), &diagnostic)
	if len(shape) > 0 {
		field("请求形状", shape, &diagnostic)
	}
	hasBreakdown := isImage && len(attempts) > 0 && (len(attempts) > 1 || requested > 1)
	for _, attempt := range attempts {
		if isImage && object(attempt)["result_status"] == "generated_but_delivery_failed" {
			hasBreakdown = true
		}
	}
	timelineView := timeline(timings, array(monitor["events"]), safeMetrics(value("timings_ms")))
	groups := []any{}
	if hasBreakdown {
		timelineView = timeline(map[string]any{}, nil)
		groups = attemptGroups(attempts, requested)
	}
	view["detail_presentation"] = map[string]any{"primary_fields": primary, "diagnostic_fields": diagnostic, "diagnostic_notice": missingDiagnosticNotice(result, view["upstream_error"], view["upstream_text"]), "auto_expand_timeline": result == "failed" || ms >= 180000 || integer(timings["stream_error_ms"]) > 0, "timeline": timelineView, "has_attempt_breakdown": hasBreakdown, "attempt_groups": groups}
	return view, nil
}
