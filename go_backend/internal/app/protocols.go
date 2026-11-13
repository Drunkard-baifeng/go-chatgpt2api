package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/chat"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
	"chatgpt2api/go_backend/internal/usage"
	"github.com/gin-gonic/gin"
)

func registerProtocolRoutes(router *gin.Engine, options Options) {
	// 大型参考图输入在进入生成领域前需要识别协议；只允许两个有界解析器同时读取。
	parsing := make(chan struct{}, 2)
	for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/search"} {
		router.POST(path, func(c *gin.Context) {
			if path == "/v1/messages" && c.GetHeader("Authorization") == "" && c.GetHeader("X-Api-Key") != "" {
				c.Request.Header.Set("Authorization", "Bearer "+c.GetHeader("X-Api-Key"))
			}
			subject := textSubject(c, options.Auth, false)
			if subject == nil {
				return
			}
			delivery := newResponseDelivery(c)
			defer delivery.controller.SetWriteDeadline(time.Time{})
			defer delivery.controller.SetReadDeadline(time.Time{})
			request, err := readProtocolRequest(c, path, parsing)
			fail := func(err error) {
				e := chat.PublicError(err)
				if path == "/v1/messages" {
					writeError(c, e.Status, chatErrorBody(e)["type"].(string), e.Message)
				} else {
					writeChatError(c, e)
				}
			}
			if err != nil {
				fail(err)
				return
			}
			var random [16]byte
			if _, err = rand.Read(random[:]); err != nil {
				fail(chat.ErrUnavailable)
				return
			}
			r := &protocolResponse{responseDelivery: delivery, kind: path, stream: request.Stream, tools: request.Tools, id: hex.EncodeToString(random[:]), model: request.Text.Model, created: time.Now().Unix(), request: request.Text}
			if request.Image != nil {
				request.Image.BaseURL = imageRequestBaseURL(c.Request, options.Config.TrustedProxies...)
				if options.Images == nil {
					writeImageError(c, images.PublicError(images.ErrUnavailable))
					return
				}
				r.model = request.Image.Model
				_, err = options.Images.Execute(c.Request.Context(), *subject, func(context.Context) (images.Request, error) { return *request.Image, nil }, r.imageComplete)
			} else {
				if options.Text == nil {
					fail(chat.ErrUnavailable)
					return
				}
				_, err = options.Text.Execute(c.Request.Context(), *subject, request.Text, r.delta, r.complete)
			}
			if err == nil {
				return
			}
			if r.delivered {
				options.Logger.Error("调用终态保存失败", "request_id", c.Writer.Header().Get("X-Request-ID"), "code", chat.PublicError(err).Code)
				return
			}
			if c.Request.Context().Err() != nil {
				return
			}
			if !c.Writer.Written() {
				if request.Image != nil {
					writeImageError(c, images.PublicError(err))
				} else {
					fail(err)
				}
				return
			}
			if r.stream {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
				defer cancel()
				e := chatErrorBody(chat.PublicError(err))
				if request.Image != nil {
					failure := images.PublicError(err)
					e = gin.H{"type": "server_error", "code": failure.Code, "message": failure.Message}
				}
				_ = r.event(cleanup, gin.H{"type": "error", "error": e})
				if path == "/v1/responses" {
					_ = r.send(cleanup, []byte("data: [DONE]\n\n"))
				}
			}
		})
	}
}

func readProtocolRequest(c *gin.Context, path string, slots chan struct{}) (result chat.ProtocolRequest, err error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		return result, chat.ErrBusy
	}
	limit := int64(chat.MaxRequestBytes)
	if path == "/v1/responses" {
		limit = images.MaxRequestBytes
	}
	err = managementInput(c, c.Request.Context(), limit, func(context.Context) error {
		switch path {
		case "/v1/responses":
			result, err = chat.ParseResponses(c.Request.Body)
		case "/v1/messages":
			result, err = chat.ParseMessages(c.Request.Body)
		case "/v1/search":
			result.Text, err = chat.ParseSearch(c.Request.Body)
		}
		return err
	})
	if errors.Is(err, images.ErrInput) {
		err = &chat.InputError{Message: "请求读取失败或超过大小限制"}
	}
	return result, err
}

type protocolResponse struct {
	*responseDelivery
	kind, id, model string
	created         int64
	stream, started bool
	opened          bool
	output          strings.Builder
	tools           []chat.Tool
	request         chat.Request
	usage           map[string]any
}

func (r *protocolResponse) event(ctx context.Context, payload gin.H) error {
	kind, _ := payload["type"].(string)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.c.Header("Content-Type", "text/event-stream; charset=utf-8")
	r.c.Header("Cache-Control", "no-cache")
	r.c.Header("X-Accel-Buffering", "no")
	prefix := "event: " + kind + "\ndata: "
	if r.kind == "/v1/responses" {
		prefix = "data: "
		if !r.opened {
			prefix = ": stream-open\n\n" + prefix
			r.opened = true
		}
	}
	return r.send(ctx, append(append([]byte(prefix), data...), []byte("\n\n")...))
}
func (r *protocolResponse) response(status string, items []gin.H) gin.H {
	out := gin.H{"id": "resp_" + r.id, "object": "response", "created_at": r.created, "status": status, "error": nil, "incomplete_details": nil, "model": r.model, "output": items, "parallel_tool_calls": false}
	if status == "completed" && r.usage != nil {
		out["usage"] = r.usage
	}
	return out
}
func (r *protocolResponse) item(text, status string, annotations []gin.H) gin.H {
	if annotations == nil {
		annotations = []gin.H{}
	}
	return gin.H{"id": "msg_" + r.id, "type": "message", "status": status, "role": "assistant", "content": []gin.H{{"type": "output_text", "text": text, "annotations": annotations}}}
}
func (r *protocolResponse) start(ctx context.Context) error {
	if r.started {
		return nil
	}
	r.started = true
	if r.kind == "/v1/responses" {
		if err := r.event(ctx, gin.H{"type": "response.created", "response": r.response("in_progress", []gin.H{})}); err != nil {
			return err
		}
		if err := r.event(ctx, gin.H{"type": "response.output_item.added", "output_index": 0, "item": r.item("", "in_progress", nil)}); err != nil {
			return err
		}
		return nil
	}
	inputText, inputImage := r.request.InputTokens()
	if err := r.event(ctx, gin.H{"type": "message_start", "message": gin.H{"id": "msg_" + r.id, "type": "message", "role": "assistant", "model": r.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": gin.H{"input_tokens": inputText + inputImage, "output_tokens": 0}}}); err != nil {
		return err
	}
	if len(r.tools) > 0 {
		return nil
	}
	return r.event(ctx, gin.H{"type": "content_block_start", "index": 0, "content_block": gin.H{"type": "text", "text": ""}})
}
func (r *protocolResponse) delta(ctx context.Context, result chat.Result, text string) error {
	r.output.WriteString(text)
	if !r.stream || len(r.tools) > 0 {
		return nil
	}
	if err := r.start(ctx); err != nil {
		return err
	}
	if r.kind == "/v1/responses" {
		return r.event(ctx, gin.H{"type": "response.output_text.delta", "item_id": "msg_" + r.id, "output_index": 0, "content_index": 0, "delta": text})
	}
	return r.event(ctx, gin.H{"type": "content_block_delta", "index": 0, "delta": gin.H{"type": "text_delta", "text": text}})
}
func (r *protocolResponse) json(ctx context.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.c.Header("Content-Type", "application/json; charset=utf-8")
	if err = r.send(ctx, data); err == nil {
		r.delivered = true
	}
	return err
}
func (r *protocolResponse) complete(ctx context.Context, result chat.Result) error {
	if r.kind == "/v1/search" {
		return r.json(ctx, result.Search)
	}
	text := r.output.String()
	annotations := []gin.H{}
	extra := []gin.H{}
	if result.Search.ConversationID != "" {
		text, annotations = searchCitations(result.Search)
		sources := []gin.H{}
		for _, s := range result.Search.Sources {
			sources = append(sources, gin.H{"type": "url", "url": s.URL})
		}
		query := ""
		for _, m := range r.request.Messages {
			if m.Role == "user" {
				query = m.Content
			}
		}
		extra = append(extra, gin.H{"id": "ws_" + r.id, "type": "web_search_call", "status": "completed", "action": gin.H{"type": "search", "query": query, "queries": []string{query}, "sources": sources}})
	}
	if r.kind == "/v1/messages" {
		return r.messageComplete(ctx, text)
	}
	inputText, inputImage := r.request.InputTokens()
	r.usage = usage.Tokens(inputText, inputImage, usage.Text(text, r.model), 0)
	item := r.item(text, "completed", annotations)
	items := append(extra, item)
	if !r.stream {
		return r.json(ctx, r.response("completed", items))
	}
	if result.Search.ConversationID != "" {
		r.started = true
		search := extra[0]
		searchID := search["id"]
		if err := r.event(ctx, gin.H{"type": "response.created", "response": r.response("in_progress", []gin.H{})}); err != nil {
			return err
		}
		pending := gin.H{"id": searchID, "type": "web_search_call", "status": "in_progress", "action": search["action"]}
		for _, ev := range []gin.H{{"type": "response.output_item.added", "output_index": 0, "item": pending}, {"type": "response.web_search_call.in_progress", "output_index": 0, "item_id": searchID}, {"type": "response.web_search_call.searching", "output_index": 0, "item_id": searchID}, {"type": "response.web_search_call.completed", "output_index": 0, "item_id": searchID}, {"type": "response.output_item.done", "output_index": 0, "item": search}, {"type": "response.output_item.added", "output_index": 1, "item": r.item("", "in_progress", annotations)}, {"type": "response.output_text.delta", "item_id": "msg_" + r.id, "output_index": 1, "content_index": 0, "delta": text}} {
			if err := r.event(ctx, ev); err != nil {
				return err
			}
		}
	}
	if err := r.start(ctx); err != nil {
		return err
	}
	index := len(extra)
	for _, event := range []gin.H{{"type": "response.output_text.done", "item_id": "msg_" + r.id, "output_index": index, "content_index": 0, "text": text}, {"type": "response.output_item.done", "output_index": index, "item": item}} {
		if err := r.event(ctx, event); err != nil {
			return err
		}
	}
	if err := r.event(ctx, gin.H{"type": "response.completed", "response": r.response("completed", items)}); err != nil {
		return err
	}
	if err := r.send(ctx, []byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	r.delivered = true
	return nil
}
func (r *protocolResponse) imageComplete(ctx context.Context, result images.Result) error {
	r.usage = result.Usage
	items := []gin.H{}
	for i, output := range result.Data {
		if output.B64JSON == "" {
			return images.ErrOutput
		}
		items = append(items, gin.H{"id": "ig_" + r.id + "_" + string(rune('0'+i)), "type": "image_generation_call", "status": "completed", "result": output.B64JSON, "revised_prompt": output.RevisedPrompt})
	}
	if len(items) == 0 {
		return images.ErrOutput
	}
	if !r.stream {
		return r.json(ctx, r.response("completed", items))
	}
	if err := r.event(ctx, gin.H{"type": "response.created", "response": r.response("in_progress", []gin.H{})}); err != nil {
		return err
	}
	for i, item := range items {
		if err := r.event(ctx, gin.H{"type": "response.output_item.done", "output_index": i, "item": item}); err != nil {
			return err
		}
	}
	if err := r.event(ctx, gin.H{"type": "response.completed", "response": r.response("completed", items)}); err != nil {
		return err
	}
	if err := r.send(ctx, []byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	r.delivered = true
	return nil
}
func (r *protocolResponse) messageComplete(ctx context.Context, text string) error {
	inputText, inputImage := r.request.InputTokens()
	outputTokens := usage.Text(text, r.model)
	blocks, reason := messageBlocks(text, r.tools)
	if !r.stream {
		return r.json(ctx, gin.H{"id": "msg_" + r.id, "type": "message", "role": "assistant", "model": r.model, "content": blocks, "stop_reason": reason, "stop_sequence": nil, "usage": gin.H{"input_tokens": inputText + inputImage, "output_tokens": outputTokens}})
	}
	if err := r.start(ctx); err != nil {
		return err
	}
	textBlock := len(r.tools) == 0 || blocks[0]["type"] == "text"
	if len(r.tools) > 0 {
		// 有工具时先缓冲，避免把工具标记作为用户文本发出。
		if blocks[0]["type"] == "text" {
			if err := r.event(ctx, gin.H{"type": "content_block_start", "index": 0, "content_block": gin.H{"type": "text", "text": ""}}); err != nil {
				return err
			}
			if err := r.event(ctx, gin.H{"type": "content_block_delta", "index": 0, "delta": gin.H{"type": "text_delta", "text": blocks[0]["text"]}}); err != nil {
				return err
			}
			blocks = blocks[1:]
		}
	}
	if textBlock {
		if err := r.event(ctx, gin.H{"type": "content_block_stop", "index": 0}); err != nil {
			return err
		}
	}
	if len(r.tools) > 0 {
		for i, block := range blocks {
			index := i
			if textBlock {
				index++
			}
			delta := gin.H{"type": "text_delta", "text": block["text"]}
			start := gin.H{"type": "text", "text": ""}
			if block["type"] == "tool_use" {
				start = gin.H{"type": "tool_use", "id": block["id"], "name": block["name"], "input": gin.H{}}
				data, _ := json.Marshal(block["input"])
				delta = gin.H{"type": "input_json_delta", "partial_json": string(data)}
			}
			for _, ev := range []gin.H{{"type": "content_block_start", "index": index, "content_block": start}, {"type": "content_block_delta", "index": index, "delta": delta}, {"type": "content_block_stop", "index": index}} {
				if err := r.event(ctx, ev); err != nil {
					return err
				}
			}
		}
	}
	if err := r.event(ctx, gin.H{"type": "message_delta", "delta": gin.H{"stop_reason": reason, "stop_sequence": nil}, "usage": gin.H{"output_tokens": outputTokens}}); err != nil {
		return err
	}
	if err := r.event(ctx, gin.H{"type": "message_stop", "created": r.created}); err != nil {
		return err
	}
	r.delivered = true
	return nil
}
func searchCitations(result upstream.SearchResult) (string, []gin.H) {
	text := result.Answer
	annotations := []gin.H{}
	if len(result.Sources) > 0 {
		text += "\n\nSources:\n"
	}
	for _, s := range result.Sources {
		if s.Title != "" {
			text += s.Title + " - "
		}
		start := len([]rune(text))
		text += s.URL
		annotations = append(annotations, gin.H{"type": "url_citation", "start_index": start, "end_index": len([]rune(text)), "url": s.URL, "title": s.Title})
		text += "\n"
	}
	return strings.TrimSpace(text), annotations
}
func messageBlocks(text string, tools []chat.Tool) ([]gin.H, string) {
	fallback := []gin.H{{"type": "text", "text": text}}
	if len(tools) == 0 {
		return fallback, "end_turn"
	}
	start := strings.Index(text, "<tool_calls>")
	end := strings.Index(text, "</tool_calls>")
	if start < 0 || end < start {
		return fallback, "end_turn"
	}
	end += len("</tool_calls>")
	var root struct {
		Calls []struct {
			Name   string `xml:"tool_name"`
			Params struct {
				Inner string `xml:",innerxml"`
				Text  string `xml:",chardata"`
			} `xml:"parameters"`
		} `xml:"tool_call"`
	}
	decoder := xml.NewDecoder(strings.NewReader(text[start:end]))
	if decoder.Decode(&root) != nil || len(root.Calls) == 0 || len(root.Calls) > 32 {
		return fallback, "end_turn"
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	blocks := []gin.H{}
	prefix := strings.TrimSpace(text[:start] + text[end:])
	if prefix != "" {
		blocks = append(blocks, gin.H{"type": "text", "text": prefix})
	}
	for _, call := range root.Calls {
		if !names[call.Name] {
			return fallback, "end_turn"
		}
		params := map[string]any{}
		inner := strings.TrimSpace(call.Params.Inner)
		decodedText := strings.TrimSpace(call.Params.Text)
		if strings.HasPrefix(decodedText, "{") {
			if json.Unmarshal([]byte(decodedText), &params) != nil || params == nil {
				return fallback, "end_turn"
			}
		} else {
			// 支持 Python 原协议的逐参数 XML 与 JSON 对象两种形式。
			d := xml.NewDecoder(strings.NewReader("<root>" + inner + "</root>"))
			depth := 0
			for {
				token, err := d.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return fallback, "end_turn"
				}
				switch tok := token.(type) {
				case xml.StartElement:
					depth++
					if depth == 2 {
						var value string
						if d.DecodeElement(&value, &tok) != nil {
							return fallback, "end_turn"
						}
						depth--
						var decoded any
						if json.Unmarshal([]byte(value), &decoded) != nil {
							decoded = value
						}
						if _, ok := params[tok.Name.Local]; ok {
							return fallback, "end_turn"
						}
						params[tok.Name.Local] = decoded
					}
				case xml.EndElement:
					depth--
				}
			}
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return fallback, "end_turn"
		}
		blocks = append(blocks, gin.H{"type": "tool_use", "id": "toolu_" + hex.EncodeToString(random[:]), "name": call.Name, "input": params})
	}
	return blocks, "tool_use"
}
