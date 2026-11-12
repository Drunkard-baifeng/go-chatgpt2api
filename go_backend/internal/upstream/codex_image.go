package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
)

type CodexImageRequest struct {
	Prompt, Size, Quality, OutputFormat string
	Inputs                              []ImageInput
	StreamWindow                        time.Duration
	Validate                            func([]byte) error
}

// GenerateCodexImage 对齐 Python 的 Codex Responses 生图协议。该端点直接
// 接收内联参考图，不使用网页 Sentinel、文件上传或可续查的图片会话。
// 一次调用只提交一次，任何提交状态未知的失败都禁止上层换号重放。
func (s *Session) GenerateCodexImage(ctx context.Context, token string, req CodexImageRequest) (result ImageResult, err error) {
	if !safeHeaderToken(token) || req.Prompt == "" || req.StreamWindow <= 0 || req.Validate == nil || len(req.Inputs) > 16 {
		return result, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	content := []any{map[string]any{"type": "input_text", "text": req.Prompt}}
	inputBytes := int64(0)
	for _, input := range req.Inputs {
		inputBytes += int64(len(input.Data))
		if len(input.Data) == 0 || int64(len(input.Data)) > assettransfer.MaxBytes || inputBytes > 2*assettransfer.MaxBytes {
			return result, ErrResponseTooLarge
		}
		media, _, parseErr := mime.ParseMediaType(input.Media)
		if parseErr != nil || !strings.HasPrefix(media, "image/") {
			return result, ErrProtocol
		}
		content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + media + ";base64," + base64.StdEncoding.EncodeToString(input.Data)})
	}
	format := strings.ToLower(strings.TrimSpace(req.OutputFormat))
	switch format {
	case "jpg":
		format = "jpeg"
	case "gif", "png", "jpeg", "webp":
	default:
		format = "png"
	}
	size, quality := req.Size, req.Quality
	if size == "" {
		size = "1024x1024"
	}
	if quality == "" {
		quality = "auto"
	}
	action := "generate"
	if len(req.Inputs) != 0 {
		action = "edit"
	}
	payload := map[string]any{
		"model": "gpt-5.5", "instructions": "Use the image_generation tool to create exactly one image for the user's request. Return the generated image result.",
		"store": false, "stream": true,
		"input":       []any{map[string]any{"role": "user", "content": content}},
		"tools":       []any{map[string]any{"type": "image_generation", "model": "gpt-image-2", "action": action, "size": size, "quality": quality, "output_format": format}},
		"tool_choice": map[string]any{"type": "image_generation"},
	}
	encoded, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		return result, ErrProtocol
	}
	stream, cancel := context.WithTimeout(ctx, req.StreamWindow)
	defer cancel()
	// Do 无法证明传输失败前上游没有受理；只有明确 HTTP 拒绝和结构化
	// Responses 终态才允许按已知失败交给执行者处理。
	known := false
	defer func() {
		if err != nil && !known {
			err = errors.Join(ErrImageSubmissionUnknown, err)
		}
	}()
	finish := callrecord.Span(stream, "generation_start", "generation_start_ms")
	response, err := s.Do(stream, http.MethodPost, "/backend-api/codex/responses", bytes.NewReader(encoded), http.Header{
		"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"},
	})
	if err != nil {
		finish(err)
		return result, err
	}
	defer response.Body.Close()
	if err = responseStatus(response, "image_conversation"); err != nil {
		known = true
		finish(err)
		return result, err
	}
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || (media != "text/event-stream" && media != "application/json") {
		err = protocolResponseError(response, "codex_image_response")
		finish(err)
		return result, err
	}
	finish(nil)
	// 内联图片事件可以大于网页 pointer SSE 的 1 MiB，但仍受会话总量
	// 和现有单图 50 MiB 预算约束，不允许配置放大到无限单事件分配。
	limit := min(s.policy.MaxResponseBytes, int64(base64.StdEncoding.EncodedLen(assettransfer.MaxBytes))+(1<<20))
	state := codexImageState{ctx: stream, validate: req.Validate, limit: int(limit)}
	if media == "application/json" {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err == nil && int64(len(data)) > limit {
			err = ErrResponseTooLarge
		}
		if err == nil {
			_, err = state.event(data, "")
		}
	} else {
		err = state.read(response.Body)
	}
	if err != nil {
		return result, err
	}
	known = state.terminal || state.failure != nil || len(state.data) != 0
	if len(state.data) != 0 {
		result.Data = state.data
		result.PostSuccessFailure = state.failure
		return result, nil
	}
	if state.failure != nil {
		return result, state.failure
	}
	return result, &Failure{Stage: "image_stream", Code: "no_image_generated", Status: 502, RawError: "Codex Responses 已结束，但未返回通过校验的图片结果"}
}

type codexImageState struct {
	ctx                  context.Context
	validate             func([]byte) error
	limit, events, nodes int
	data                 []byte
	failure              error
	terminal             bool
}

func (s *codexImageState) read(reader io.Reader) (err error) {
	diagnostics := beginSSEDiagnostics(s.ctx, "image")
	defer func() { diagnostics.finish(err) }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), s.limit+2)
	var data strings.Builder
	eventType := ""
	dispatch := func() (bool, error) {
		value, kind := strings.TrimSpace(data.String()), eventType
		data.Reset()
		eventType = ""
		if value == "" && kind != "error" {
			return false, nil
		}
		diagnostics.event()
		return s.event([]byte(value), kind)
	}
	for scanner.Scan() {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if len(line) > s.limit {
			return ErrResponseTooLarge
		}
		if line == "" {
			done, err := dispatch()
			if done || err != nil {
				return err
			}
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			if data.Len()+len(value)+1 > s.limit {
				return ErrResponseTooLarge
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) || errors.Is(err, ErrResponseTooLarge) {
			return ErrResponseTooLarge
		}
		diagnostics.transportError(err)
		return safeError(s.ctx, err)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	_, err = dispatch()
	return err
}

func (s *codexImageState) event(data []byte, kind string) (bool, error) {
	s.events++
	if s.events > 10000 {
		return false, ErrResponseTooLarge
	}
	if string(data) == "[DONE]" && kind != "error" {
		s.terminal = true
		return true, nil
	}
	if len(data) == 0 && kind == "error" {
		s.failure = &Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502}
		return true, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := s.decode(decoder, 0)
	if err == nil {
		if _, end := decoder.Token(); end != io.EOF {
			err = ErrProtocol
		}
	}
	if err != nil {
		return false, withDiagnostic(err, "", "", "", "codex_image_stream：SSE/JSON 事件无效或超过结构预算")
	}
	v, ok := value.(map[string]any)
	if !ok {
		if kind == "error" {
			s.failure = withDiagnostic(&Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502}, failureMessage(value, 0), diagnosticJSON(value), "", "")
			return true, nil
		}
		return false, nil
	}
	if len(s.data) == 0 {
		s.images(v)
	}
	// 报错时不保存内联图字节；错误正文仍复用统一脱敏和4000字符摘录。
	safe := codexDiagnosticValue(v).(map[string]any)
	failure := imageStreamFailure(safe, 0)
	if failure == nil {
		failure = codexToolFailure(safe)
	}
	typeName, _ := v["type"].(string)
	status, _ := v["status"].(string)
	failed := kind == "error" || typeName == "error" || typeName == "response.failed" || typeName == "response.incomplete" || status == "failed" || status == "incomplete"
	if failure == nil && failed {
		code := "image_tool_error"
		if kind == "error" || typeName == "error" {
			code = "upstream_event_error"
		}
		failure = imageFailureDetail(safe, &Failure{Stage: "image_stream", Code: code, Status: 502})
	}
	var terminalFailure *Failure
	if failed && errors.As(failure, &terminalFailure) {
		// 结构化分类可能位于 response.error 中；诊断仍保留整个脱敏终态，
		// 便于区分 response.failed、incomplete 与底层错误对象。
		terminalFailure.UpstreamError = diagnosticJSON(safe)
	}
	if failure != nil {
		s.failure = failure
	}
	s.terminal = failed || typeName == "response.completed" || status == "completed"
	return failed || typeName == "response.completed", nil
}

// Decode 逐个 token 施加深度和累计节点预算，避免完整反序列化后才发现
// 恶意嵌套；标准 JSON decoder 仍拥有语法、转义和数字格式校验。
func (s *codexImageState) decode(decoder *json.Decoder, depth int) (any, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 32 {
		return nil, ErrProtocol
	}
	s.nodes++
	if s.nodes > 100000 {
		return nil, ErrResponseTooLarge
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrProtocol
	}
	switch token {
	case json.Delim('{'):
		value := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, ErrProtocol
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrProtocol
			}
			s.nodes++
			item, err := s.decode(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			value[name] = item
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrProtocol
		}
		return value, nil
	case json.Delim('['):
		value := []any{}
		for decoder.More() {
			item, err := s.decode(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrProtocol
		}
		return value, nil
	}
	if _, delimiter := token.(json.Delim); delimiter {
		return nil, ErrProtocol
	}
	return token, nil
}

func (s *codexImageState) images(value any) {
	if len(s.data) != 0 {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "image_generation_call" {
			if result, ok := v["result"].(string); ok {
				result = strings.TrimSpace(result)
				if strings.HasPrefix(result, "data:image/") {
					_, result, _ = strings.Cut(result, ",")
				}
				if len(result) > base64.StdEncoding.EncodedLen(assettransfer.MaxBytes) {
					return
				}
				if data, err := base64.StdEncoding.DecodeString(result); err == nil && len(data) != 0 && len(data) <= assettransfer.MaxBytes && s.validate(data) == nil {
					s.data = data
					return
				}
			}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			s.images(v[key])
			if len(s.data) != 0 {
				return
			}
		}
	case []any:
		for _, item := range v {
			s.images(item)
			if len(s.data) != 0 {
				return
			}
		}
	}
}

func codexDiagnosticValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			if key == "result" && v["type"] == "image_generation_call" {
				result[key] = "[图片数据已隐藏]"
			} else {
				result[key] = codexDiagnosticValue(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for index, item := range v {
			result[index] = codexDiagnosticValue(item)
		}
		return result
	}
	return value
}

// Responses 的工具失败也可以位于 item 或 response.output 中；只把
// 明确的图片工具状态视为失败，普通 metadata.status 不参与分类。
func codexToolFailure(value any) error {
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "image_generation_call" {
			if failure := imageStreamFailure(v, 0); failure != nil {
				return failure
			}
			if v["status"] == "failed" || v["status"] == "incomplete" {
				return imageFailureDetail(v, &Failure{Stage: "image_stream", Code: "image_tool_error", Status: 502})
			}
		}
		for _, key := range []string{"item", "output", "response"} {
			if failure := codexToolFailure(v[key]); failure != nil {
				return failure
			}
		}
	case []any:
		for _, item := range v {
			if failure := codexToolFailure(item); failure != nil {
				return failure
			}
		}
	}
	return nil
}
