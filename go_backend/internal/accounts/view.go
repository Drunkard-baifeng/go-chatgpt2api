package accounts

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	var result map[string]any
	if len(parts) != 3 || len(parts[1]) > 90000 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	_ = json.Unmarshal(data, &result)
	return result
}
func timestamp(value any) any {
	if n := number(value); n > 0 {
		if n > 10000000000 {
			n /= 1000
		}
		return n
	}
	raw := text(value)
	for _, format := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(format, raw); err == nil {
			return t.Unix()
		}
	}
	return nil
}

var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+|(?:access_token|refresh_token|id_token|password|authorization)["'\s:=]+)[^\s,;"'}]+`)
var urlCredentials = regexp.MustCompile(`(?i)(https?|socks5h?)://[^\s/]+@`)

func safeDiagnostic(value any, m map[string]any) string {
	out := text(value)
	for _, key := range []string{"access_token", "refresh_token", "id_token", "proxy"} {
		if secret := text(m[key]); secret != "" {
			out = strings.ReplaceAll(out, secret, "[已隐藏]")
		}
	}
	out = bearerPattern.ReplaceAllString(out, "[已隐藏]")
	out = urlCredentials.ReplaceAllString(out, "proxy://[已隐藏]@")
	if len(out) > 2000 {
		out = string([]rune(out)[:min(500, len([]rune(out)))])
	}
	return out
}
func lifecycle(m map[string]any) (string, string, string) {
	at := "valid"
	jwt := claims(text(m["access_token"]))
	exp := number(jwt["exp"])
	if text(m["access_token"]) == "" || text(m["last_remote_check_result"]) == "invalid" || (exp > 0 && int64(exp) <= time.Now().Unix()) {
		at = "invalid"
	} else if exp > 0 && int64(exp)-time.Now().Unix() <= 86400 {
		at = "expiring"
	}
	rt := "valid"
	if text(m["refresh_token"]) == "" {
		rt = "missing"
	} else if timestamp(m["refresh_token_invalid_at"]) != nil {
		rt = "invalid"
	}
	availability := "usable"
	if at == "invalid" {
		availability = "unavailable"
		if rt == "valid" {
			availability = "recoverable"
		}
	}
	return at, rt, availability
}
func category(m map[string]any) string {
	status := text(m["status"])
	if !flag(m["enabled"], true) {
		return "disabled"
	}
	switch status {
	case "禁用":
		return "disabled"
	case "异常":
		return "abnormal"
	case "限流":
		return "limited"
	}
	if status != "" && status != "正常" {
		return "abnormal"
	}
	_, _, available := lifecycle(m)
	if available == "unavailable" {
		return "abnormal"
	}
	return "normal"
}
func eligible(m map[string]any, image bool) bool {
	status := text(m["status"])
	if status == "" {
		status = "正常"
	}
	if status != "正常" && (!(!image && status == "限流")) {
		return false
	}
	if !flag(m["enabled"], true) || text(m["last_remote_check_result"]) == "pending" {
		return false
	}
	_, _, available := lifecycle(m)
	return available != "unavailable"
}
func proxyProjection(raw string) (string, string, string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "global" {
		return "", "inherit", "", "继承默认出口"
	}
	if raw == "direct" {
		return "direct", "direct", "", "直连"
	}
	if strings.HasPrefix(raw, "group:") {
		id := strings.TrimPrefix(raw, "group:")
		return raw, "group", id, "代理组：" + id
	}
	return "custom", "custom", "", "自定义代理"
}
func project(m map[string]any, groups []map[string]any, inflight int) map[string]any {
	id := accountID(m)
	source := normalizeSource(text(m["source_type"]))
	sourceLabel := "Web"
	if source == "codex" {
		sourceLabel = "Codex"
	}
	plan := text(m["type"])
	planLabel := map[string]string{"free": "Free", "plus": "Plus", "pro": "Pro", "prolite": "ProLite", "team": "Team", "business": "Team", "enterprise": "Enterprise"}[strings.ToLower(plan)]
	if planLabel == "" {
		planLabel = plan
	}
	if planLabel == "" {
		planLabel = "未知"
	}
	backend := text(m["status"])
	if backend == "" {
		backend = "正常"
	}
	cat := category(m)
	labels := map[string]string{"normal": "正常", "limited": "限流", "abnormal": "异常", "disabled": "禁用"}
	tones := map[string]string{"normal": "success", "limited": "warning", "abnormal": "error", "disabled": "neutral"}
	at, rt, availability := lifecycle(m)
	reasonCode, reason := "", "账号已启用，当前凭据可调用"
	rawError := safeDiagnostic(first(m["last_remote_check_error"], m["last_token_refresh_error"], m["last_refresh_error"]), m)
	switch {
	case backend == "禁用":
		reasonCode, reason = "disabled", "账号已手动禁用"
	case backend == "异常":
		reasonCode, reason = "auth_invalid", "远程确认账号登录态已失效"
	case cat == "abnormal":
		reasonCode, reason = "credentials_unavailable", "AT 已失效，且没有可用 RT，账号无法参与调度"
	case backend == "限流":
		reasonCode, reason = "image_quota_exhausted", "远程确认图片额度已用完"
	case text(m["last_remote_check_result"]) == "pending":
		reasonCode, reason = "verification_pending", "正在核验账号状态"
	case text(m["last_remote_check_result"]) == "error" || rawError != "":
		reasonCode, reason = "remote_check_failed", "最近一次账号检测失败，尚未确认账号失效"
	case availability == "recoverable":
		reasonCode, reason = "access_token_refresh_required", "AT 已失效，将使用 RT 自动刷新"
	}
	quota := max(0, number(m["quota"]))
	unknown := flag(m["image_quota_unknown"], true)
	unlimited := unknown && (strings.EqualFold(plan, "pro") || strings.EqualFold(plan, "prolite"))
	quotaState, quotaLabel := "available", strconv.Itoa(quota)
	if unlimited {
		quotaState, quotaLabel = "unlimited", "无限"
	} else if unknown {
		quotaState, quotaLabel = "unknown", "未知"
	} else if quota == 0 {
		quotaState = "exhausted"
	}
	groupID := text(m["group_id"])
	groupName := ""
	groupEnabled := true
	for _, group := range groups {
		if text(group["id"]) == groupID {
			groupName = text(group["name"])
			groupEnabled = flag(group["enabled"], true)
		}
	}
	proxyRaw := text(m["proxy"])
	if proxyRaw == "" && text(m["proxy_group_id"]) != "" {
		proxyRaw = "group:" + text(m["proxy_group_id"])
	}
	proxy, proxyMode, proxyGroup, proxyLabel := proxyProjection(proxyRaw)
	atPresent := map[string][2]string{"valid": {"AT 有效", "success"}, "expiring": {"AT 临期", "warning"}, "invalid": {"AT 失效", "error"}}[at]
	rtPresent := map[string][2]string{"valid": {"RT 有效", "success"}, "missing": {"RT 缺失", "neutral"}, "invalid": {"RT 失效", "error"}}[rt]
	availabilityPresent := map[string][2]string{"usable": {"可用", "success"}, "recoverable": {"可恢复", "warning"}, "unavailable": {"不可用", "error"}}[availability]
	enabled := backend != "禁用" && flag(m["enabled"], true)
	action, actionLabel := "disable", "停用账号"
	if !enabled {
		action, actionLabel = "enable", "恢复启用"
	}
	jwt := claims(text(m["access_token"]))
	return map[string]any{"id": id, "email": text(m["email"]), "user_id": text(m["user_id"]), "display_name": first(m["email"], m["user_id"], id), "plan": plan, "plan_label": planLabel, "source": source, "source_label": sourceLabel, "source_plan_label": sourceLabel + " / " + planLabel, "backend_status": backend, "status_category": cat, "status_label": labels[cat], "status_tone": tones[cat], "status_reason_code": reasonCode, "status_reason": reason, "status_raw_error": rawError, "enabled": enabled, "enabled_action": action, "enabled_action_label": actionLabel, "available": eligible(m, true) && groupEnabled, "access_token_status": at, "access_token_label": atPresent[0], "access_token_tone": atPresent[1], "access_token_issued_at": timestamp(jwt["iat"]), "access_token_expires_at": timestamp(jwt["exp"]), "refresh_token_status": rt, "refresh_token_label": rtPresent[0], "refresh_token_tone": rtPresent[1], "can_refresh_access_token": rt == "valid", "credential_availability": availability, "credential_availability_label": availabilityPresent[0], "credential_availability_tone": availabilityPresent[1], "refresh_token_invalid_at": timestamp(m["refresh_token_invalid_at"]), "last_token_refresh_at": timestamp(m["last_token_refresh_at"]), "last_token_refresh_error": safeDiagnostic(m["last_token_refresh_error"], m), "last_token_refresh_error_at": timestamp(m["last_token_refresh_error_at"]), "quota_remaining": quota, "quota_unknown": unknown, "quota_unlimited": unlimited, "quota_state": quotaState, "quota_label": quotaLabel, "quota_reset_at": timestamp(m["restore_at"]), "group_id": groupID, "group_name": groupName, "proxy": proxy, "proxy_mode": proxyMode, "proxy_group_id": proxyGroup, "proxy_label": proxyLabel, "success_count": max(0, number(m["success"])), "failure_count": max(0, number(m["fail"])), "image_inflight": inflight, "last_remote_check_result": text(m["last_remote_check_result"]), "last_remote_check_attempt_at": timestamp(m["last_remote_check_attempt_at"]), "last_remote_checked_at": timestamp(m["last_remote_checked_at"]), "created_at": timestamp(m["created_at"]), "last_used_at": timestamp(m["last_used_at"])}
}
