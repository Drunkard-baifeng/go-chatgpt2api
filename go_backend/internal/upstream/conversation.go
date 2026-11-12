package upstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type Message struct {
	Role    string
	Content string
	Images  []ImageInput
	// LoadImages defers remote sources until the selected account's proxy is known.
	LoadImages func(context.Context, string) ([]ImageInput, error)
}
type ConversationResult struct {
	ConversationID string
	MessageID      string
	ResolvedModel  string
}

var ErrProtocol = errors.New("上游文本协议无效或未正常结束")
var ErrChallenge = errors.New("上游验证挑战未能完成")

// Failure 分类由应用生成，仅携带有界脱敏的上游原因摘录。
type Failure struct {
	Stage                                          string
	Code                                           string
	Status                                         int
	Message, UpstreamError, UpstreamText, RawError string
	RetryAfter                                     time.Duration
	HasRetryAfter                                  bool
}

func (e *Failure) Error() string { return "上游请求失败：" + e.Stage + " / " + e.Code }

// SSE 每个事件和每行最多 1 MiB；总量由 Session 限制。
// 完成前 EOF、显式 error 及空结果均不能转换为成功响应。
func ReadConversation(ctx context.Context, reader io.Reader, emit func(string) error) (ConversationResult, error) {
	return readConversation(ctx, reader, emit, nil)
}

func readConversation(ctx context.Context, reader io.Reader, emit func(string) error, historyIDs map[string]bool) (result ConversationResult, err error) {
	diagnostics := beginSSEDiagnostics(ctx, "text")
	defer func() { diagnostics.finish(err) }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	state := textState{emit: emit, result: &result, historyIDs: historyIDs}
	var data strings.Builder
	eventType := ""
	dispatch := func() (bool, error) {
		defer func() { data.Reset(); eventType = "" }()
		value := strings.TrimSuffix(data.String(), "\n")
		if value != "" {
			diagnostics.event()
		}
		if eventType == "error" {
			return false, &Failure{Stage: "conversation", Code: "upstream_event_error", Status: 502}
		}
		if value == "" {
			return false, nil
		}
		if !utf8.ValidString(value) {
			return false, ErrProtocol
		}
		if value == "[DONE]" {
			if strings.TrimSpace(state.text) == "" || !state.final {
				return false, ErrProtocol
			}
			return true, nil
		}
		var event map[string]any
		if json.Unmarshal([]byte(value), &event) != nil || event == nil {
			return false, ErrProtocol
		}
		if err := state.event(event, 0); err != nil {
			return false, err
		}
		return false, nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := dispatch()
			if done || err != nil {
				return result, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			if data.Len()+len(value)+1 > 1<<20 {
				return result, ErrResponseTooLarge
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		diagnostics.transportError(err)
		return result, safeError(ctx, err)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	// ChatGPT 可能在 message.end_turn 后关闭 SSE；明确成功终态才接受。
	if data.Len() > 0 {
		done, err := dispatch()
		if done || err != nil {
			return result, err
		}
	}
	if state.final && strings.TrimSpace(state.text) != "" {
		return result, nil
	}
	diagnostics.transportError(io.ErrUnexpectedEOF)
	return result, ErrProtocol
}

type textState struct {
	historyIDs map[string]bool
	result     *ConversationResult
	emit       func(string) error
	text       string
	currentID  string
	role       string
	channel    string
	recipient  string
	final      bool
}

func (s *textState) event(event map[string]any, depth int) error {
	if depth > 16 {
		return ErrProtocol
	}
	if value, ok := event["error"]; ok && value != nil && value != false {
		return &Failure{Stage: "conversation", Code: "upstream_event_error", Status: 502}
	}
	if kind, _ := event["type"].(string); kind == "error" {
		return &Failure{Stage: "conversation", Code: "upstream_event_error", Status: 502}
	}
	if id, _ := event["conversation_id"].(string); id != "" {
		if len(id) > 255 {
			return ErrProtocol
		}
		s.result.ConversationID = id
	}
	if message, ok := event["message"].(map[string]any); ok {
		return s.message(message)
	}
	if value, ok := event["v"].(map[string]any); ok {
		if _, exists := value["message"]; exists {
			return s.event(value, depth+1)
		}
	}
	path, _ := event["p"].(string)
	op, _ := event["o"].(string)
	if op == "patch" {
		patches, ok := event["v"].([]any)
		if !ok {
			return ErrProtocol
		}
		for _, raw := range patches {
			patch, ok := raw.(map[string]any)
			if !ok {
				return ErrProtocol
			}
			if err := s.event(patch, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if path == "/message" && (op == "replace" || op == "add") {
		if value, ok := event["v"].(map[string]any); ok {
			return s.message(value)
		}
		return ErrProtocol
	}
	if path == "/message/channel" {
		s.channel, _ = event["v"].(string)
		return nil
	}
	if path == "/message/author/role" {
		s.role, _ = event["v"].(string)
		return nil
	}
	if path == "/message/recipient" {
		s.recipient, _ = event["v"].(string)
		return nil
	}
	if path == "/message/status" {
		status, _ := event["v"].(string)
		if status == "finished_successfully" && s.visible() {
			s.final = true
		}
		if status == "failed" || status == "error" {
			return ErrProtocol
		}
		return nil
	}
	if path == "/message/end_turn" {
		if event["v"] == true && s.visible() {
			s.final = true
		}
		return nil
	}
	if !s.visible() {
		return nil
	}
	value, ok := event["v"].(string)
	if !ok {
		return nil
	}
	if path == "/message/content/parts/0" || path == "/message/content/text" {
		switch op {
		case "append":
			return s.update(s.text + value)
		case "replace", "add":
			return s.update(value)
		}
	}
	if path == "" && op == "" && s.text != "" {
		return s.update(s.text + value)
	}
	return nil
}

func (s *textState) visible() bool {
	return !s.historyIDs[s.currentID] && s.role == "assistant" && (s.channel == "" || s.channel == "final") && (s.recipient == "" || s.recipient == "all")
}
func (s *textState) message(message map[string]any) error {
	author, _ := message["author"].(map[string]any)
	role, _ := author["role"].(string)
	channel, _ := message["channel"].(string)
	id, _ := message["id"].(string)
	if id == "" || len(id) > 255 {
		return ErrProtocol
	}
	// 用户、工具和 analysis 都会成为当前 patch 目标，防止泄漏为答案。
	s.role = role
	s.channel = channel
	s.recipient, _ = message["recipient"].(string)
	if s.currentID != id {
		if s.text != "" && s.visible() {
			return ErrProtocol
		}
		s.currentID = id
		s.final = false
	}
	if !s.visible() {
		return nil
	}
	s.result.MessageID = id
	if metadata, ok := message["metadata"].(map[string]any); ok {
		for _, key := range []string{"resolved_model_slug", "model_slug"} {
			if model, ok := metadata[key].(string); ok && len(model) <= 255 {
				s.result.ResolvedModel = model
				break
			}
		}
	}
	status, _ := message["status"].(string)
	if status == "failed" || status == "error" {
		return ErrProtocol
	}
	content, _ := message["content"].(map[string]any)
	if kind, _ := content["content_type"].(string); kind != "text" {
		return nil
	}
	var text strings.Builder
	if parts, ok := content["parts"].([]any); ok {
		for _, part := range parts {
			value, ok := part.(string)
			if !ok {
				return ErrProtocol
			}
			text.WriteString(value)
		}
	} else if value, ok := content["text"].(string); ok {
		text.WriteString(value)
	}
	if err := s.update(text.String()); err != nil {
		return err
	}
	if status == "finished_successfully" || message["end_turn"] == true {
		s.final = true
	}
	return nil
}
func (s *textState) update(text string) error {
	if len(text) > 4<<20 || !utf8.ValidString(text) || !strings.HasPrefix(text, s.text) {
		return ErrProtocol
	}
	delta := text[len(s.text):]
	s.text = text
	if delta != "" {
		return s.emit(delta)
	}
	return nil
}
