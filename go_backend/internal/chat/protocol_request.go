package chat

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
)

// ProtocolRequest 仅映射已执行的兼容输入；所有执行仍交给原领域生命周期。
type ProtocolRequest struct {
	Text   Request
	Image  *images.Request
	Stream bool
	Tools  []Tool
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

const unavailableToolHint = "This compatibility backend cannot execute local tools, shell commands, non-search tools, or file operations. Do not claim to have run tools or inspected external resources. If a user asks you to use a tool, say that tool execution is unavailable through this backend."

func ParseChatRequest(reader io.Reader) (ProtocolRequest, error) {
	var out ProtocolRequest
	body, err := protocolBody(reader, images.MaxRequestBytes)
	if err != nil {
		return out, err
	}
	out.Stream, err = protocolBool(body["stream"])
	if err != nil {
		return out, err
	}
	model, _ := strictString(body["model"])
	isImage := images.SupportedModel(model)
	if isImage {
		model = strings.ToLower(strings.TrimSpace(model))
	}
	var modalities []string
	_ = json.Unmarshal(body["modalities"], &modalities)
	for _, kind := range modalities {
		if strings.EqualFold(strings.TrimSpace(kind), "image") {
			isImage = true
		}
	}
	if !isImage {
		data, _ := json.Marshal(body)
		out.Text, err = ParseRequest(bytes.NewReader(data))
		out.Text.Endpoint = "/v1/chat/completions"
		search, unsupported := toolKinds(body)
		out.Text.Search = search && !unsupported
		if unsupported {
			out.Text.Messages = append([]upstream.Message{{Role: "system", Content: unavailableToolHint}}, out.Text.Messages...)
		}
		return out, err
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-image-2"
	}
	prompt, _ := strictString(body["prompt"])
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(body["messages"], &messages)
	parts := []string{}
	refs := []images.Source{}
	for _, m := range messages {
		role, _ := strictString(m["role"])
		if role != "user" {
			continue
		}
		if text := messageContentString(m["content"]); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
		if sources, e := messageImageSources(m["content"]); e != nil {
			return out, e
		} else if len(sources) > 0 {
			refs = sources
		}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = strings.Join(parts, "\n")
	}
	payload := map[string]any{"prompt": prompt, "model": model, "response_format": "b64_json"}
	for _, key := range []string{"n", "output_format", "frame_count", "frame_duration_ms"} {
		if v, ok := body[key]; ok && string(v) != "null" {
			payload[key] = v
		}
	}
	data, _ := json.Marshal(payload)
	r, _ := http.NewRequest("POST", "/", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	image, e := images.ParseRequest(r, false)
	if e != nil {
		return out, invalid("图片请求参数无效")
	}
	image.Sources = refs
	image.Edit = len(refs) > 0
	image.Endpoint = "/v1/chat/completions"
	out.Image = &image
	return out, nil
}

func toolKinds(body map[string]json.RawMessage) (search, unsupported bool) {
	var tools []map[string]json.RawMessage
	_ = json.Unmarshal(body["tools"], &tools)
	for _, tool := range tools {
		kind, _ := strictString(tool["type"])
		switch kind {
		case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
			search = true
		case "image_generation":
		default:
			unsupported = true
		}
	}
	return
}

func protocolBody(r io.Reader, limit int64) (map[string]json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	var body map[string]json.RawMessage
	if err != nil || int64(len(data)) > limit || json.Unmarshal(data, &body) != nil || body == nil {
		return nil, invalid("请求必须是大小受限的 JSON 对象")
	}
	return body, nil
}
func onlyFields(body map[string]json.RawMessage, fields string) error {
	allowed := make(map[string]bool)
	for _, field := range strings.Fields(fields) {
		allowed[field] = true
	}
	for k := range body {
		if !allowed[k] {
			return invalid("请求包含不支持的参数")
		}
	}
	return nil
}
func strictString(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
		return "", invalid("字段必须是字符串")
	}
	return s, nil
}
func protocolBool(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var b bool
	if string(raw) != "null" && json.Unmarshal(raw, &b) != nil {
		return false, invalid("stream 必须是布尔值")
	}
	return b, nil
}
func canonicalText(body map[string]json.RawMessage, messages []upstreamMessage, endpoint string) (Request, error) {
	out := map[string]any{"messages": messages}
	for _, k := range []string{"model", "stream", "thinking_effort", "reasoning_effort", "reasoning"} {
		if raw, ok := body[k]; ok {
			out[k] = raw
		}
	}
	data, _ := json.Marshal(out)
	req, err := ParseRequest(bytes.NewReader(data))
	req.Endpoint = endpoint
	return req, err
}

type upstreamMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func messageContentString(content any) string {
	if value, ok := content.(string); ok {
		return value
	}
	data, ok := content.(json.RawMessage)
	if !ok {
		data, _ = json.Marshal(content)
	}
	text, _ := textContent(data)
	return text
}

func contentText(raw json.RawMessage, response bool) (string, error) {
	if value, err := strictString(raw); err == nil {
		return value, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", invalid("消息必须包含文本内容")
	}
	var out strings.Builder
	for _, part := range parts {
		kind, _ := strictString(part["type"])
		if kind != "text" && !(response && (kind == "input_text" || kind == "output_text")) {
			return "", invalid("此操作仅支持文本内容")
		}
		text, err := strictString(part["text"])
		if err != nil {
			return "", err
		}
		out.WriteString(text)
	}
	return out.String(), nil
}

func ParseResponses(reader io.Reader) (ProtocolRequest, error) {
	var out ProtocolRequest
	body, err := protocolBody(reader, images.MaxRequestBytes)
	if err != nil {
		return out, err
	}
	out.Stream, err = protocolBool(body["stream"])
	if err != nil {
		return out, err
	}
	operation := "text"
	var imageTool map[string]json.RawMessage
	if raw, ok := body["tools"]; ok {
		var tools []map[string]json.RawMessage
		if string(raw) != "null" && (json.Unmarshal(raw, &tools) != nil || len(tools) > 64) {
			return out, invalid("tools 必须为数组")
		}
		for _, tool := range tools {
			kind, _ := strictString(tool["type"])
			switch kind {
			case "image_generation":
				operation = "image"
				imageTool = tool
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
				if operation != "image" {
					operation = "search"
				}
			default:
				// Python accepts unknown tools and adds a tool-unavailable hint.
			}
		}
	}
	if raw, ok := body["tool_choice"]; ok {
		var choice map[string]any
		_ = json.Unmarshal(raw, &choice)
		if choice["type"] == "image_generation" {
			operation = "image"
		}
	}
	messages := []upstreamMessage{}
	if raw, ok := body["instructions"]; ok {
		value, e := strictString(raw)
		if string(raw) == "null" {
			value, e = "", nil
		}
		if e != nil {
			return out, e
		}
		if value != "" {
			messages = append(messages, upstreamMessage{"system", value})
		}
	}
	refs := []images.Source{}
	appendContent := func(role string, raw json.RawMessage) error {
		if operation != "image" {
			messages = append(messages, upstreamMessage{Role: role, Content: raw})
			return nil
		}
		if value, e := strictString(raw); e == nil {
			messages = append(messages, upstreamMessage{role, value})
			return nil
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
			return invalid("input 内容无效")
		}
		var text strings.Builder
		for _, p := range parts {
			kind, _ := strictString(p["type"])
			switch kind {
			case "input_text", "output_text", "text":
				v, e := strictString(p["text"])
				if e != nil {
					return e
				}
				text.WriteString(v)
			case "input_image", "image_url", "image":
				encoded, _ := json.Marshal([]any{p})
				sources, e := messageImageSources(encoded)
				if e != nil {
					return e
				}
				if len(sources) > 0 {
					refs = sources[len(sources)-1:]
				}
			default:
				return invalid("图片请求包含不支持的内容")
			}
		}
		if text.Len() > 0 && role == "user" {
			messages = append(messages, upstreamMessage{role, text.String()})
		}
		return nil
	}
	raw := body["input"]
	var single map[string]json.RawMessage
	if json.Unmarshal(raw, &single) == nil && single != nil {
		raw, _ = json.Marshal([]any{single})
	}
	if value, e := strictString(raw); e == nil {
		messages = append(messages, upstreamMessage{"user", value})
	} else {
		var input []map[string]json.RawMessage
		if json.Unmarshal(raw, &input) != nil || len(input) == 0 || len(input) > 128 {
			return out, invalid("input 必须是文本或消息数组")
		}
		for _, item := range input {
			kind, _ := strictString(item["type"])
			if kind == "input_text" || kind == "output_text" || kind == "text" || kind == "input_image" || kind == "image_url" || kind == "image" {
				parts, _ := json.Marshal([]any{item})
				if e := appendContent("user", parts); e != nil {
					return out, e
				}
				continue
			}
			if kind != "" && kind != "message" {
				return out, invalid("不支持的 input 类型")
			}
			role := "user"
			if v, ok := item["role"]; ok {
				role, err = strictString(v)
				if err != nil {
					return out, err
				}
			}
			if err = appendContent(role, item["content"]); err != nil {
				return out, err
			}
		}
	}
	if operation == "image" {
		var prompt strings.Builder
		for _, m := range messages {
			if m.Role != "user" {
				continue
			}
			if prompt.Len() > 0 {
				prompt.WriteString("\n")
			}
			prompt.WriteString(messageContentString(m.Content))
		}
		payload := map[string]any{"prompt": prompt.String(), "response_format": "b64_json", "n": 1}
		if v, ok := body["model"]; ok && string(v) != "null" && strings.TrimSpace(messageContentString(v)) != "" {
			payload["model"] = v
		}
		for _, k := range []string{"size", "quality", "output_format"} {
			if v, ok := imageTool[k]; ok {
				payload[k] = v
			}
		}
		data, _ := json.Marshal(payload)
		r, _ := http.NewRequest("POST", "/", bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		image, e := images.ParseRequest(r, false)
		if e != nil {
			return out, invalid("图片工具参数无效")
		}
		image.Endpoint = "/v1/responses"
		image.Sources = refs
		image.Edit = len(refs) > 0
		out.Image = &image
		return out, nil
	}
	out.Text, err = canonicalText(body, messages, "/v1/responses")
	_, unsupported := toolKinds(body)
	out.Text.Search = operation == "search" && !unsupported
	if unsupported {
		out.Text.Messages = append([]upstream.Message{{Role: "system", Content: unavailableToolHint}}, out.Text.Messages...)
	}
	return out, err
}

func ParseSearch(reader io.Reader) (Request, error) {
	body, err := protocolBody(reader, MaxRequestBytes)
	if err != nil {
		return Request{}, err
	}
	prompt, err := strictString(body["prompt"])
	if err != nil || strings.TrimSpace(prompt) == "" {
		return Request{}, invalid("prompt 必须为非空文本")
	}
	req, err := canonicalText(map[string]json.RawMessage{"model": json.RawMessage(`"gpt-5-5"`)}, []upstreamMessage{{"user", prompt}}, "/v1/search")
	req.Search = true
	return req, err
}

func ParseMessages(reader io.Reader) (ProtocolRequest, error) {
	var out ProtocolRequest
	body, err := protocolBody(reader, MaxRequestBytes)
	if err != nil {
		return out, err
	}
	out.Stream, err = protocolBool(body["stream"])
	if err != nil {
		return out, err
	}
	if raw, ok := body["tools"]; ok {
		var declarations []map[string]any
		if string(raw) != "null" && (json.Unmarshal(raw, &declarations) != nil || len(declarations) > 64) {
			return out, invalid("tools 无效")
		}
		for _, d := range declarations {
			fn, _ := d["function"].(map[string]any)
			name, _ := d["name"].(string)
			if name == "" {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				continue
			}
			desc, _ := d["description"].(string)
			if desc == "" {
				desc, _ = fn["description"].(string)
			}
			schema, _ := d["input_schema"].(map[string]any)
			if schema == nil {
				schema, _ = d["parameters"].(map[string]any)
			}
			if schema == nil {
				schema, _ = fn["parameters"].(map[string]any)
			}
			if schema == nil {
				schema = map[string]any{}
			}
			out.Tools = append(out.Tools, Tool{Name: name, Description: desc, InputSchema: schema})
		}
	}
	messages := []upstreamMessage{}
	system := ""
	if raw, ok := body["system"]; ok {
		system, err = textContent(raw)
		if err != nil {
			return out, err
		}
	}
	if len(out.Tools) > 0 {
		encoded, _ := json.Marshal(out.Tools)
		system += "\nAvailable tools: " + string(encoded) + "\nTo request a tool, output only <tool_calls><tool_call><tool_name>NAME</tool_name><parameters>{JSON object}</parameters></tool_call></tool_calls>. Do not claim tools were executed."
	}
	if system != "" {
		messages = append(messages, upstreamMessage{"system", system})
	}
	var input []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &input) != nil || len(input) == 0 || len(input) > 128 {
		return out, invalid("messages 必须为非空数组")
	}
	for _, item := range input {
		role := "user"
		if v, exists := item["role"]; exists {
			role, _ = strictString(v)
		}
		raw := item["content"]
		text, e := strictString(raw)
		if e != nil {
			var blocks []map[string]json.RawMessage
			if string(raw) == "null" || len(raw) == 0 {
				messages = append(messages, upstreamMessage{role, ""})
				continue
			}
			if json.Unmarshal(raw, &blocks) != nil {
				return out, invalid("消息内容无效")
			}
			var b strings.Builder
			prepared := []any{}
			for _, block := range blocks {
				kind, _ := strictString(block["type"])
				switch kind {
				case "text", "input_text", "output_text":
					v, e := strictString(block["text"])
					if e != nil {
						return out, e
					}
					b.WriteString(v)
				case "tool_use":
					name, _ := strictString(block["name"])
					var args map[string]any
					if json.Unmarshal(block["input"], &args) != nil || args == nil {
						return out, invalid("工具输入必须是对象")
					}
					encoded, _ := json.Marshal(args)
					b.WriteString("<tool_calls><tool_call><tool_name>" + name + "</tool_name><parameters>")
					_ = xml.EscapeText(&b, encoded)
					b.WriteString("</parameters></tool_call></tool_calls>")
				case "tool_result":
					id, _ := strictString(block["tool_use_id"])
					v := messageContentString(block["content"])
					fmt.Fprintf(&b, "Tool result %s: %s", id, v)
				case "image", "image_url", "input_image":
					prepared = append(prepared, block)
				default:
					// Unconsumed content blocks have no text, as in Python.
				}
			}
			text = b.String()
			if len(prepared) > 0 {
				prepared = append([]any{map[string]any{"type": "text", "text": text}}, prepared...)
				messages = append(messages, upstreamMessage{role, prepared})
				continue
			}
		}
		messages = append(messages, upstreamMessage{role, text})
	}
	out.Text, err = canonicalText(body, messages, "/v1/messages")
	return out, err
}
func validToolIdentifier(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if c != '_' && c != '-' && c != '.' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
