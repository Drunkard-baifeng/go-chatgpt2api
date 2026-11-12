// Package chat 拥有文本请求、模型目录和调用生命周期。
package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
	"chatgpt2api/go_backend/internal/usage"
)

const MaxRequestBytes = 1 << 20

type Request struct {
	Endpoint            string
	Search              bool
	Model               string
	UpstreamModel       string
	Messages            []upstream.Message
	Effort              string
	Stream              bool
	ReferenceImageCount int
}

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func invalid(message string) error  { return &InputError{message} }

// Public compatibility inputs follow Python: unconsumed SDK options are ignored.
func ParseRequest(reader io.Reader) (Request, error) {
	var result Request
	data, err := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return result, invalid("请求读取失败或超过 1 MiB")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return result, invalid("请求必须是一个 JSON 对象")
	}
	result.Model = "auto"
	if raw, ok := body["model"]; ok {
		if string(raw) != "null" && json.Unmarshal(raw, &result.Model) != nil {
			return result, invalid("model 必须是字符串")
		}
	}
	result.Model = strings.TrimSpace(result.Model)
	if result.Model == "" {
		result.Model = "auto"
	}
	if raw, ok := body["stream"]; ok {
		if string(raw) != "null" && json.Unmarshal(raw, &result.Stream) != nil {
			return result, invalid("stream 必须是布尔值")
		}
	}
	var effort json.RawMessage
	if raw, ok := body["thinking_effort"]; ok {
		effort = raw
	} else if raw, ok := body["reasoning_effort"]; ok {
		effort = raw
	} else if raw, ok := body["reasoning"]; ok {
		var reasoning map[string]json.RawMessage
		_ = json.Unmarshal(raw, &reasoning)
		effort = reasoning["effort"]
	}
	if len(effort) > 0 && string(effort) != "null" {
		var value string
		_ = json.Unmarshal(effort, &value)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "none":
		case "low", "medium", "high":
			result.Effort = strings.ToLower(strings.TrimSpace(value))
		case "xhigh", "extended":
			result.Effort = "extended"
		}
	}
	result.UpstreamModel, err = executionModel(result.Model, result.Effort)
	if err != nil {
		return result, err
	}
	if raw, ok := body["messages"]; ok {
		var messages []map[string]json.RawMessage
		if string(raw) == "null" || json.Unmarshal(raw, &messages) != nil || len(messages) == 0 || len(messages) > 128 {
			return result, invalid("messages 必须包含 1 到 128 条文本消息")
		}
		for _, message := range messages {
			role := "user"
			if raw, exists := message["role"]; exists && json.Unmarshal(raw, &role) != nil {
				return result, invalid("消息缺少 role")
			}
			content, err := textContent(message["content"])
			if err != nil {
				return result, err
			}
			m := upstream.Message{Role: role, Content: content}
			if role == "user" {
				sources, e := messageImageSources(message["content"])
				if e != nil {
					return result, e
				}
				if len(sources) > 0 {
					result.ReferenceImageCount += len(sources)
					m.LoadImages = func(ctx context.Context, proxy string) ([]upstream.ImageInput, error) {
						inputs, e := images.ReadInputs(ctx, sources, proxy)
						if e != nil {
							return nil, e
						}
						out := make([]upstream.ImageInput, 0, len(inputs))
						for _, in := range inputs {
							out = append(out, upstream.ImageInput{Data: in.Data, Media: in.Media, Name: in.Name, Width: in.Width, Height: in.Height})
						}
						return out, nil
					}
				}
			}
			result.Messages = append(result.Messages, m)
		}
	} else {
		var prompt string
		if json.Unmarshal(body["prompt"], &prompt) != nil || strings.TrimSpace(prompt) == "" {
			return result, invalid("必须提供 messages 或 prompt")
		}
		result.Messages = []upstream.Message{{Role: "user", Content: prompt}}
	}
	return result, nil
}

func textContent(raw json.RawMessage) (string, error) {
	if string(raw) == "null" || len(raw) == 0 {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", invalid("本阶段只支持文本内容")
	}
	var out strings.Builder
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" && part.Type != "output_text" {
			continue
		}
		out.WriteString(part.Text)
	}
	return out.String(), nil
}

func (r Request) InputTokens() (int, int) {
	textTokens, imageTokens := 3, 0
	for _, m := range r.Messages {
		textTokens += 3 + usage.Text(m.Role, r.Model) + usage.Text(m.Content, r.Model)
		for _, in := range m.Images {
			imageTokens += usage.ImageInput(in.Width, in.Height)
		}
	}
	return textTokens, imageTokens
}

func messageImageSources(raw json.RawMessage) ([]images.Source, error) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, nil
	}
	values := []any{}
	for _, p := range parts {
		var kind string
		_ = json.Unmarshal(p["type"], &kind)
		if kind != "image_url" && kind != "input_image" && kind != "image" {
			continue
		}
		var value any
		for _, key := range []string{"image_url", "url"} {
			if v, ok := p[key]; ok && json.Unmarshal(v, &value) == nil {
				break
			}
		}
		if value == nil {
			var source map[string]any
			_ = json.Unmarshal(p["source"], &source)
			if source != nil {
				value = source["data"]
				if source["type"] == "url" {
					value = source["url"]
				}
			}
		}
		if value != nil {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	sources, err := images.ParseSources(values)
	if err != nil {
		return nil, invalid("图片输入无效")
	}
	return sources, nil
}

type ModelCatalog struct {
	Object        string   `json:"object"`
	SchemaVersion int      `json:"schema_version"`
	GeneratedAt   string   `json:"generated_at"`
	Revision      string   `json:"revision"`
	ChatModels    []string `json:"chat_models"`
	ImageModels   []string `json:"image_models"`
	AllModels     []string `json:"all_models"`
	Defaults      struct {
		ChatModel  string `json:"chat_model"`
		ImageModel string `json:"image_model"`
	} `json:"defaults"`
	Capabilities struct {
		ImageUpscale              bool     `json:"image_upscale"`
		HighResolutionImageModels []string `json:"high_resolution_image_models"`
		GIFModels                 []string `json:"gif_models"`
		ImageMaxN                 int      `json:"image_max_n"`
	} `json:"capabilities"`
	Source struct {
		Chat  string `json:"chat"`
		Image string `json:"image"`
	} `json:"source"`
	OpenAIModelsEndpoint string `json:"openai_models_endpoint"`
}

func Catalog() ModelCatalog {
	names := make([]string, 0, len(textModels))
	for _, model := range textModels {
		names = append(names, model)
	}
	c := ModelCatalog{Object: "model_catalog", SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Revision: "go-sync-images:4", ChatModels: names, ImageModels: append([]string{}, images.Models...), AllModels: append(append([]string{}, names...), images.Models...), OpenAIModelsEndpoint: "/v1/models"}
	c.Defaults.ChatModel = "auto"
	c.Defaults.ImageModel = "gpt-image-2"
	c.Capabilities.ImageMaxN = images.MaxN
	c.Source.Chat = "fallback"
	c.Source.Image = "fallback"
	c.Capabilities.HighResolutionImageModels = []string{}
	c.Capabilities.GIFModels = []string{}
	return c
}

// WithImageAccounts follows Python's local account-derived model catalog.
func (c ModelCatalog) WithImageAccounts(available bool, plans []string) ModelCatalog {
	if available {
		c.Source.Image = "accounts"
	}
	seen := map[string]bool{}
	for _, plan := range plans {
		seen[plan] = true
	}
	if seen["plus"] || seen["team"] || seen["pro"] {
		c.ImageModels = append(c.ImageModels, images.CodexModel)
		c.Capabilities.HighResolutionImageModels = append(c.Capabilities.HighResolutionImageModels, images.CodexModel)
	}
	for _, plan := range []string{"plus", "team", "pro"} {
		if !seen[plan] {
			continue
		}
		model := plan + "-" + images.CodexModel
		c.ImageModels = append(c.ImageModels, model)
		c.Capabilities.HighResolutionImageModels = append(c.Capabilities.HighResolutionImageModels, model)
	}
	c.AllModels = append(append([]string{}, c.ChatModels...), c.ImageModels...)
	return c
}

// WithRuntimeSettings projects the persisted shared GIF/SVG capability list.
// Only models already advertised in this image catalog may expose raw formats.
func (c ModelCatalog) WithRuntimeSettings(values map[string]any) ModelCatalog {
	if n, ok := values["image_max_n"].(float64); ok && n >= 1 && n <= images.MaxN {
		c.Capabilities.ImageMaxN = int(n)
	}
	if enabled, ok := values["image_upscale_enabled"].(bool); ok {
		c.Capabilities.ImageUpscale = enabled
	}
	var models []string
	switch configured := values["gif_models"].(type) {
	case []any:
		for _, value := range configured {
			if model, ok := value.(string); ok {
				models = append(models, model)
			}
		}
	case []string:
		models = configured
	}
	c.Capabilities.GIFModels = []string{}
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(strings.ToLower(model))
		if seen[model] {
			continue
		}
		for _, available := range c.ImageModels {
			if model == available {
				c.Capabilities.GIFModels = append(c.Capabilities.GIFModels, model)
				seen[model] = true
				break
			}
		}
	}
	projection, _ := json.Marshal(map[string]any{"capabilities": c.Capabilities, "image_models": c.ImageModels, "source": c.Source})
	digest := sha256.Sum256(projection)
	c.Revision = "go-sync-images:5:" + hex.EncodeToString(digest[:8])
	return c
}

var ErrUnavailable = errors.New("文本服务暂不可用")

// 与 Python 一样，文本模型名和归一化思考参数直接传给上游；目录提供推荐值。
var textModels = []string{"gpt-5.6", "auto"}

func executionModel(model, effort string) (string, error) {
	if model == "" || len(model) > 255 || strings.ContainsAny(model, "\r\n\x00") {
		return "", invalid("model 无效")
	}
	switch effort {
	case "", "low", "medium", "high", "extended":
		return model, nil
	default:
		return "", invalid("思考参数未归一化")
	}
}
