package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
)

// FailureDetails 的正文在采集时即脱敏、有界；错误分类始终来自结构化字段。
type FailureDetails struct{ Message, UpstreamError, UpstreamText, RawError string }
type diagnosedError struct {
	cause   error
	details FailureDetails
}

func (e *diagnosedError) Error() string { return e.cause.Error() }
func (e *diagnosedError) Unwrap() error { return e.cause }

func DiagnosticDetails(err error) FailureDetails {
	return diagnosticDetails(err, 0)
}
func diagnosticDetails(err error, depth int) FailureDetails {
	var result FailureDetails
	if err == nil || depth > 32 {
		return result
	}
	merge := func(d FailureDetails) {
		if message := publicFailureText(d.Message); message != "" {
			result.Message = message
		}
		if d.UpstreamError != "" {
			result.UpstreamError = d.UpstreamError
		}
		if d.UpstreamText != "" {
			result.UpstreamText = d.UpstreamText
		}
		if d.RawError != "" {
			result.RawError = d.RawError
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			merge(diagnosticDetails(child, depth+1))
		}
	} else if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		merge(diagnosticDetails(wrapped.Unwrap(), depth+1))
	}
	if f, ok := err.(*Failure); ok {
		merge(FailureDetails{f.Message, f.UpstreamError, f.UpstreamText, f.RawError})
	}
	if f, ok := err.(*assettransfer.Failure); ok {
		stage := "图片下载"
		if f.Method == "PUT" {
			stage = "图片上传"
		}
		raw := stage + "：" + f.Phase + "，" + f.Reason
		if f.Status != 0 {
			raw += fmt.Sprintf("，HTTP %d", f.Status)
		}
		message := ""
		if f.Status == 400 || f.Status == 429 {
			var body any
			if json.Unmarshal([]byte(f.Body), &body) == nil {
				message = failureMessage(body, 0)
			}
		}
		merge(FailureDetails{Message: message, UpstreamError: callrecord.CaptureDiagnosticText(f.Body), RawError: raw})
	}
	if d, ok := err.(*diagnosedError); ok {
		if result.Message == "" && d.details.Message != "" {
			result.Message = d.details.Message
		}
		if result.UpstreamError == "" && d.details.UpstreamError != "" {
			result.UpstreamError = d.details.UpstreamError
		}
		if result.UpstreamText == "" && d.details.UpstreamText != "" {
			result.UpstreamText = d.details.UpstreamText
		}
		if result.RawError == "" && d.details.RawError != "" {
			result.RawError = d.details.RawError
		}
	}
	return result
}

func withDiagnostic(err error, message, upstreamError, text, raw string) error {
	if err == nil {
		return nil
	}
	return &diagnosedError{cause: err, details: FailureDetails{
		publicFailureText(message), callrecord.CaptureDiagnosticText(upstreamError),
		callrecord.CaptureDiagnosticText(text), callrecord.CaptureDiagnosticText(raw),
	}}
}

func diagnosticJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return callrecord.CaptureDiagnosticText(string(data))
}

var bareFailureCode = regexp.MustCompile(`(?i)^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$`)

func publicFailureText(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	candidate := text
	if len(candidate) >= 6 && strings.HasPrefix(candidate, "```") && strings.HasSuffix(candidate, "```") {
		candidate = strings.TrimSpace(candidate[3 : len(candidate)-3])
		if strings.HasPrefix(strings.ToLower(candidate), "json") {
			candidate = strings.TrimSpace(candidate[4:])
		}
	}
	if json.Valid([]byte(candidate)) || bareFailureCode.MatchString(text) {
		return ""
	}
	switch strings.ToLower(text) {
	case "error", "fail", "failed", "limited", "限流":
		return ""
	}
	return callrecord.CaptureDiagnosticText(text)
}

// 只从明确的可读字段取公开原因，不把整个 JSON 或任意状态码当成说明。
func failureMessage(value any, depth int) string {
	if depth > 12 {
		return ""
	}
	switch v := value.(type) {
	case string:
		return publicFailureText(v)
	case map[string]any:
		for _, key := range []string{"message", "detail", "error_message", "reason", "error", "content", "parts", "text"} {
			if text := failureMessage(v[key], depth+1); text != "" {
				return text
			}
		}
	case []any:
		parts := []string{}
		for _, item := range v {
			if text := failureMessage(item, depth+1); text != "" {
				parts = append(parts, text)
			}
			if len(parts) >= 16 {
				break
			}
		}
		return publicFailureText(strings.Join(parts, "\n"))
	}
	return ""
}

func responseStatus(response *Response, stage string) error {
	err := checkStatus(response.StatusCode, stage)
	if err == nil {
		return nil
	}
	var f *Failure
	if !errors.As(err, &f) {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 65536))
	f.UpstreamError = callrecord.CaptureDiagnosticText(string(body))
	var value any
	if json.Unmarshal(body, &value) == nil {
		f.Message = failureMessage(value, 0)
	}
	f.RawError = fmt.Sprintf("%s：HTTP %d，Content-Type=%s", stage, response.StatusCode, callrecord.SafeDiagnosticText(response.Header.Get("Content-Type")))
	if delay, e := strconv.ParseFloat(response.Header.Get("Retry-After"), 64); e == nil && delay >= 0 && delay <= 86400 {
		f.RetryAfter = time.Duration(delay * float64(time.Second))
		f.HasRetryAfter = true
	} else if at, e := http.ParseTime(response.Header.Get("Retry-After")); e == nil {
		f.RetryAfter = max(0, time.Until(at))
		f.HasRetryAfter = true
	}
	return f
}

func protocolResponseError(response *Response, stage string) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 65536))
	raw := fmt.Sprintf("%s：期望 text/event-stream，实际 HTTP %d，Content-Type=%s\n%s", stage, response.StatusCode, response.Header.Get("Content-Type"), body)
	var value any
	message := ""
	if json.Unmarshal(body, &value) == nil {
		message = failureMessage(value, 0)
	}
	return withDiagnostic(ErrProtocol, message, string(body), "", raw)
}

func (s *imageState) textError() error {
	return withDiagnostic(ErrImageText, s.assistantText, "", s.assistantText, "")
}
