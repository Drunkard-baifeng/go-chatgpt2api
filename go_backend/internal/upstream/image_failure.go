package upstream

import (
	"errors"
	"strings"
)

// 按结构化上游错误码沿用 Python 的文本／失败分类；不从自由文本反算分类。
// 公开响应只使用可读原因，管理详情另外保留有界、脱敏的上游诊断。
func imageStreamFailure(value map[string]any, depth int) error {
	if depth > 16 {
		return ErrProtocol
	}
	code := ""
	status := 502
	if value["blocked"] == true {
		return imageFailureDetail(value, &Failure{Stage: "image_stream", Code: "content_policy_violation", Status: 400})
	}
	for _, key := range []string{"code", "error_code", "failure_code", "type", "error"} {
		candidate, _ := value[key].(string)
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		switch candidate {
		case "content_policy_violation", "moderation_blocked", "safety_blocked":
			code, status = "content_policy_violation", 400
		case "invalid_image_input", "upstream_text_reply":
			code, status = candidate, 400
		case "unsupported_model", "unsupported_image_model":
			code, status = "unsupported_model", 400
		case "image_quota_exhausted", "quota_exhausted", "insufficient_quota":
			code, status = "image_quota_exhausted", 429
		case "upstream_rate_limited", "rate_limit_exceeded", "file_upload_throttled":
			code, status = candidate, 429
		case "auth_invalid", "invalid_access_token", "token_invalid", "token_invalidated", "token_revoked":
			code, status = "auth_invalid", 401
		case "image_tool_error", "upstream_error", "no_image_generated":
			code, status = candidate, 502
		}
		if status == 400 {
			break
		}
	}
	if code != "" {
		failure := &Failure{Stage: "image_stream", Code: code, Status: status}
		return imageFailureDetail(value, failure)
	}
	for _, key := range []string{"error", "response", "metadata", "moderation_response"} {
		if child, ok := value[key].(map[string]any); ok {
			if err := imageStreamFailure(child, depth+1); err != nil {
				message := failureMessage(value, 0)
				var failure *Failure
				if message != "" && errors.As(err, &failure) && failure.Message == "" {
					// metadata 可能只有错误码；首次遇到具体失败正文时补齐原文。
					failure.Message, failure.UpstreamError = message, diagnosticJSON(value)
				}
				return withDiagnostic(err, message, diagnosticJSON(value), "", "")
			}
		}
	}
	return nil
}

func imageFailureDetail(value map[string]any, failure *Failure) error {
	failure.Message = failureMessage(value, 0)
	failure.UpstreamError = diagnosticJSON(value)
	if failure.Status == 400 {
		return errors.Join(ErrImageText, failure)
	}
	return failure
}
