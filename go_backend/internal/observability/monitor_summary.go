package observability

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// 所有窗口统计只使用完成请求；活动请求只贡献并发和阶段分布。
// 每条请求先完成投影，汇总复用相同的诊断快照，不重算并发图片的累计耗时。
func monitorSummary(active, recent []any, rejected int) map[string]any {
	counts := map[string]int64{}
	slow := map[string]int64{}
	samples := map[string][]int64{}
	durations := []int64{}
	for _, raw := range recent {
		r := object(raw)
		status := text(r["status"])
		if status == "partial_success" {
			counts["partial_success"]++
			status = "success"
		}
		if status == "unknown" {
			status = "failed"
		}
		counts[status]++
		ms := integer(r["duration_ms"])
		if ms > 0 {
			durations = append(durations, ms)
		}
		if ms >= 120000 {
			slow["total_over_120s"]++
		}
		metrics := object(r["timings_ms"])
		for key, rawValue := range metrics {
			if value := integer(rawValue); value > 0 {
				samples[key] = append(samples[key], value)
			}
		}
		for key, threshold := range map[string]int64{"handler_queue_ms": 1000, "stream_first_queue_ms": 1000, "account_wait_ms": 5000, "egress_wait_ms": 1000} {
			if integer(metrics[key]) >= threshold {
				slow[key[:len(key)-3]]++
			}
		}
		if _, observed := metrics["stream_error_ms"]; observed {
			counts["stream_error_requests"]++
		}
		if isLocalRejection(text(r["error_code"])) {
			slow["local_reject_or_busy"]++
		}
		if switches := integer(r["switch_count"]); switches > 0 {
			counts["account_switch_requests"]++
			counts["account_switches"] += switches
			if status == "success" {
				counts["account_switch_success"]++
			}
		}
	}
	p95 := map[string]any{}
	for key, values := range samples {
		p95[key] = percentile95(values)
	}
	result := map[string]any{"active": len(active), "completed": len(recent), "metric_p95": p95, "slow_counts": slow,
		"success": counts["success"], "partial_success": counts["partial_success"], "failed": counts["failed"], "text_review": counts["text_review"], "rate_limited": counts["rate_limited"],
		"success_rate":            monitorRatio(counts["success"]*100, counts["success"]+counts["failed"]+counts["rate_limited"]),
		"account_switch_requests": counts["account_switch_requests"], "account_switches": counts["account_switches"], "account_switch_success": counts["account_switch_success"],
		"account_switch_recovery_rate": monitorRatio(counts["account_switch_success"]*100, counts["account_switch_requests"]),
		"switch_unrecovered":           counts["account_switch_requests"] - counts["account_switch_success"], "switch_average": monitorRatio(counts["account_switches"], counts["account_switch_requests"]),
		"stream_error_requests": counts["stream_error_requests"], "admission_rejected": rejected}
	slow["local_reject_or_busy"] += int64(rejected)
	if len(durations) > 0 {
		var sum int64
		for _, ms := range durations {
			sum += ms
		}
		result["avg_duration_ms"] = int64(math.RoundToEven(float64(sum) / float64(len(durations))))
		result["p95_duration_ms"] = percentile95(durations)
	}
	return result
}

func percentile95(values []int64) int64 {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[(len(values)*95+99)/100-1]
}
func monitorRatio(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return math.Round(float64(numerator)*10/float64(denominator)) / 10
}
func isLocalRejection(code string) bool {
	switch code {
	case "image_capacity_exceeded", "text_capacity_exceeded", "upstream_account_unavailable":
		return true
	default:
		return false
	}
}

// ConfigureExecution 在启动组装时读取执行者的真实容量。图片异步任务与同步接口
// 共用图片池，不能再次把异步 worker 数量加到执行容量中。
func (s *Service) ConfigureExecution(text, image, file int, activeEgress func() int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution = map[string]int{"text": text, "image": image, "file": file, "total": text + image + file}
	s.activeEgress = activeEgress
}

// Rejected 记录未进入调用生命周期的入口满载事件，不创建虚假的数据库记录。
// 与完成窗口同期保留最近 200 次拒绝，过量时通过说明明确采样上限。
func (s *Service) Rejected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = append(s.rejected, time.Now())
	if len(s.rejected) > recentCapacity {
		copy(s.rejected, s.rejected[1:])
		s.rejected = s.rejected[:recentCapacity]
	}
}
func (s *Service) rejectionCount() int {
	cutoff := s.options.Started
	if len(s.recent) == recentCapacity {
		cutoff = s.recent[0].record.Ended
	}
	count := 0
	for _, at := range s.rejected {
		if !at.Before(cutoff) {
			count++
		}
	}
	return count
}

func diagnosticGroups(summary map[string]any, execution map[string]int, window string) []any {
	p95, slow := object(summary["metric_p95"]), summary["slow_counts"].(map[string]int64)
	item := func(key, label string, value any, meta, tone string) any {
		return map[string]any{"key": key, "label": label, "value": value, "meta": meta, "tone": tone}
	}
	count := func(key, label, meta, tone string) any { return item(key, label, summary[key], meta, tone) }
	metric := func(key, label, meta string) any {
		value := any("—")
		if ms := integer(p95[key]); ms > 0 {
			value = duration(ms)
		} else {
			meta += " · 暂无有效耗时样本"
		}
		return item(key, label, value, meta, "info")
	}
	slowItem := func(key, source, label, meta string) any { return item(key, label, slow[source], meta, "warning") }
	capacity, capacityMeta := any("—"), "执行容量尚未接入"
	if execution != nil {
		capacity = execution["total"]
		capacityMeta = fmt.Sprintf("文本 %d / 图片 %d / 文件 %d，分别限流", execution["text"], execution["image"], execution["file"])
	}
	noQueue := "Go 满载直接拒绝，不使用同步线程池排队"
	group := func(key, title, meta string, items ...any) any {
		return map[string]any{"key": key, "title": title, "meta": meta, "items": items}
	}
	avg, p95total := any("—"), any("—")
	if ms := integer(summary["avg_duration_ms"]); ms > 0 {
		avg = duration(ms)
	}
	if ms := integer(summary["p95_duration_ms"]); ms > 0 {
		p95total = duration(ms)
	}
	activeEgress := summary["active_egress_count"]
	if activeEgress == nil {
		activeEgress = "—"
	}
	return []any{
		group("overview", "实时概览", "本进程最近完成窗口，重启后清空",
			count("active", "当前并发", capacityMeta, "info"), count("completed", "完成窗口", window, "muted"), count("success", "成功数", "包含部分成功", "success"), count("failed", "失败数", "失败及未知结果", "danger"),
			count("text_review", "文本数", "返回文本，不计失败", "warning"), item("success_rate", "成功率", fmt.Sprintf("%g%%", summary["success_rate"]), "成功 /（成功 + 失败 + 限流），不含文本", "success"), slowItem("slow_total", "total_over_120s", "慢请求", "总耗时至少 120 秒"), count("rate_limited", "限流数", "完成窗口内限流", "warning")),
		group("entry_egress", "入口与出口", "实际执行限制与图片代理租约",
			item("thread_capacity", "执行容量", capacity, capacityMeta, "info"), item("handler_queue_ms", "入口排队 P95", "—", noQueue, "muted"), item("stream_first_queue_ms", "首包排队 P95", "—", noQueue, "muted"),
			item("egress_wait_ms", "出口等待 P95", "—", "出口满载直接拒绝，不等待空位", "muted"), metric("egress_acquire_ms", "出口租约 P95", "获取图片出口，包含配置读取"), item("active_egress", "活跃出口", activeEgress, "当前持有图片租约的不同出口数，包含直连", "info"),
			slowItem("egress_wait_slow", "egress_wait", "出口等待慢请求", "Go 不等待出口空位"), slowItem("local_busy", "local_reject_or_busy", "本地拒绝/繁忙", fmt.Sprintf("窗口内无号/并发；同期入口拒绝 %d 次，最多保留 200 次", integer(summary["admission_rejected"])))),
		group("account_switch", "账号与切换", "账号筛选、实际切换与恢复结果",
			metric("account_wait_ms", "账号等待 P95", "账号池筛选"), slowItem("account_wait_slow", "account_wait", "账号等待慢请求", "账号筛选至少 5 秒"), count("account_switch_requests", "切号请求", "发生过账号切换", "warning"), count("account_switches", "切换次数", "窗口内实际切换", "warning"),
			count("account_switch_success", "切号后成功", "包含部分成功", "success"), item("account_switch_recovery_rate", "切号恢复率", fmt.Sprintf("%g%%", summary["account_switch_recovery_rate"]), "切号后成功 / 切号请求", "success"), item("account_switch_unrecovered", "切号未恢复", summary["switch_unrecovered"], "切号后未成功", "warning"), item("account_switch_average", "平均切换", summary["switch_average"], "每个切号请求", "muted")),
		group("upstream_prepare", "上游准备", "完成窗口内各项耗时 P95",
			metric("upload_ms", "图片上传", "参考图上传"), metric("bootstrap_ms", "上游初始化", "ChatGPT 会话"), metric("requirements_ms", "令牌获取", "requirements / token"), metric("prepare_conversation_ms", "会话准备", "准备会话"), metric("generation_start_ms", "启动生成", "提交上游请求"), metric("http_dns_ms", "DNS 解析", "HTTP DNS P95"), metric("http_tcp_ms", "TCP 连接", "HTTP TCP P95"), metric("http_tls_ms", "TLS 握手", "HTTP TLS P95")),
		group("generation_transport", "生成与传输", "完成窗口内 HTTP、SSE 与传输中断",
			metric("http_wait_ms", "HTTP 等待", "发出请求到首包"), metric("http_ttfb_ms", "HTTP 首包", "请求开始到首包"), metric("sse_first_event_ms", "SSE 首事件", "连接后首个有效事件"), metric("sse_max_gap_ms", "SSE 最大空窗", "有效事件的最大间隔"), metric("sse_last_gap_ms", "SSE 收尾空窗", "最后事件到结束"), metric("conversation_stream_ms", "上游生成", "会话流响应"), count("stream_error_requests", "断流请求", "读取流时传输失败，不含业务拒绝和主动取消", "danger"), metric("stream_error_ms", "断流耗时 P95", "流开始到传输中断")),
		group("result_delivery", "结果与交付", "完成窗口内总耗时及各阶段 P95",
			item("average", "平均总耗时", avg, "窗口内有效总耗时均值", "info"), item("p95", "P95 总耗时", p95total, "最近秩法，缺失样本不补零", "info"), metric("poll_wait_ms", "等待结果", "间隔 / 退避"), metric("poll_request_ms", "查询结果", "task / conversation"), metric("resolve_ms", "结果处理", "file ID / 下载地址"), metric("download_ms", "图片下载", "下载上游图片"), metric("response_ms", "响应整理", "整理 API 响应"), metric("stream_ms", "协议处理 P95", "SSE 协议读取与处理")),
	}
}
