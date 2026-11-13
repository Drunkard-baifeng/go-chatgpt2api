package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/chat"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/usage"
	"github.com/gin-gonic/gin"
)

func textSubject(c *gin.Context, identity *auth.Service, admin bool) *auth.Subject {
	subject, err := identity.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		authError(c, err)
		return nil
	}
	if admin && subject.Role != "admin" {
		writeError(c, 403, "permission_error", "需要管理员权限才能执行这个操作")
		return nil
	}
	return subject
}

func imageModelCatalog(ctx context.Context, options Options) (chat.ModelCatalog, error) {
	catalog := chat.Catalog()
	if options.Accounts != nil {
		available, plans, err := options.Accounts.ImageModelAvailability(ctx)
		if err != nil {
			return catalog, err
		}
		catalog = catalog.WithImageAccounts(available, plans)
	}
	values := map[string]any{}
	if options.Settings != nil {
		var err error
		values, err = options.Settings.Runtime(ctx)
		if err != nil {
			return catalog, err
		}
	}
	return catalog.WithRuntimeSettings(values), nil
}

func registerTextRoutes(router *gin.Engine, options Options) {
	router.GET("/api/model-catalog", func(c *gin.Context) {
		if textSubject(c, options.Auth, false) == nil {
			return
		}
		catalog, err := imageModelCatalog(c.Request.Context(), options)
		if err != nil {
			writeError(c, 503, "server_error", "模型目录暂不可用")
			return
		}
		c.JSON(200, catalog)
	})
	router.GET("/v1/models", func(c *gin.Context) {
		if textSubject(c, options.Auth, false) == nil {
			return
		}
		models := []gin.H{}
		catalog, err := imageModelCatalog(c.Request.Context(), options)
		if err != nil {
			writeError(c, 503, "server_error", "模型目录暂不可用")
			return
		}
		for _, model := range catalog.AllModels {
			models = append(models, gin.H{"id": model, "object": "model", "created": 0, "owned_by": "chatgpt2api", "permission": []any{}, "root": model, "parent": nil})
		}
		c.JSON(200, gin.H{"object": "list", "data": models})
	})
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		subject := textSubject(c, options.Auth, false)
		if subject == nil {
			return
		}
		response := newTextResponse(c)
		_ = response.controller.SetReadDeadline(time.Now().Add(30 * time.Second))
		protocol, err := chat.ParseChatRequest(c.Request.Body)
		_ = response.controller.SetReadDeadline(time.Time{})
		if err != nil {
			writeChatError(c, chat.PublicError(err))
			return
		}
		request := protocol.Text
		response.stream = protocol.Stream
		response.request = request
		defer response.controller.SetWriteDeadline(time.Time{})
		if protocol.Image != nil {
			protocol.Image.BaseURL = imageRequestBaseURL(c.Request, options.Config.TrustedProxies...)
			if options.Images == nil {
				writeImageError(c, images.PublicError(images.ErrUnavailable))
				return
			}
			_, err = options.Images.Execute(c.Request.Context(), *subject, func(context.Context) (images.Request, error) { return *protocol.Image, nil }, func(ctx context.Context, result images.Result) error {
				return response.imageComplete(ctx, *protocol.Image, result)
			})
			if err != nil && !response.delivered && c.Request.Context().Err() == nil {
				writeImageError(c, images.PublicError(err))
			}
			return
		}
		if options.Text == nil {
			writeError(c, 503, "server_error", "文本服务暂不可用")
			return
		}
		_, err = options.Text.Execute(c.Request.Context(), *subject, request, response.delta, response.complete)
		if err == nil {
			return
		}
		if response.delivered {
			// 网络交付与数据库事务不可能原子提交；已完成响应后仅记录安全诊断。
			options.Logger.Error("调用终态保存失败", "request_id", c.Writer.Header().Get("X-Request-ID"), "code", chat.PublicError(err).Code)
			return
		}
		if c.Request.Context().Err() != nil {
			return
		}
		if !c.Writer.Written() {
			writeChatError(c, chat.PublicError(err))
			return
		}
		if response.stream {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
			defer cancel()
			frame, _ := json.Marshal(gin.H{"error": chatErrorBody(chat.PublicError(err))})
			_ = response.send(cleanup, append(append([]byte("data: "), frame...), []byte("\n\ndata: [DONE]\n\n")...))
		}
	})
}

// HTTP Adapter 持有序列化和下游交付；执行者通过回调把交付包含在调用总截止和终态中。
type textResponse struct {
	*responseDelivery
	stream, sentRole bool
	output           strings.Builder
	request          chat.Request
}

type responseDelivery struct {
	c          *gin.Context
	controller *http.ResponseController
	delivered  bool
}

func newTextResponse(c *gin.Context) *textResponse {
	return &textResponse{responseDelivery: newResponseDelivery(c)}
}

func newResponseDelivery(c *gin.Context) *responseDelivery {
	raw := http.ResponseWriter(c.Writer)
	// Gin 的无返回值 Flush 会吞底层错误，跳过这一层，保留真实 FlushError。
	if wrapper, ok := raw.(interface{ Unwrap() http.ResponseWriter }); ok {
		raw = wrapper.Unwrap()
	}
	return &responseDelivery{c: c, controller: http.NewResponseController(raw)}
}
func (r *textResponse) delta(ctx context.Context, result chat.Result, text string) error {
	r.output.WriteString(text)
	if !r.stream {
		return nil
	}
	if !r.c.Writer.Written() {
		r.c.Header("Content-Type", "text/event-stream; charset=utf-8")
		r.c.Header("Cache-Control", "no-cache")
		r.c.Header("X-Accel-Buffering", "no")
	}
	delta := gin.H{"content": text}
	if !r.sentRole {
		delta["role"] = "assistant"
		r.sentRole = true
	}
	frame, err := json.Marshal(chunk(result, delta, nil))
	if err != nil {
		return err
	}
	return r.send(ctx, append(append([]byte("data: "), frame...), []byte("\n\n")...))
}
func (r *textResponse) complete(ctx context.Context, result chat.Result) error {
	var annotations []gin.H
	if result.Search.ConversationID != "" {
		text, citations := searchCitations(result.Search)
		annotations = []gin.H{}
		for _, a := range citations {
			annotations = append(annotations, gin.H{"type": "url_citation", "url_citation": a})
		}
		if err := r.delta(ctx, result, text); err != nil {
			return err
		}
	}
	var data []byte
	var err error
	if r.stream {
		frame, encodeErr := json.Marshal(chunk(result, gin.H{}, "stop"))
		if encodeErr != nil {
			return encodeErr
		}
		data = append(append([]byte("data: "), frame...), []byte("\n\ndata: [DONE]\n\n")...)
	} else {
		r.c.Header("Content-Type", "application/json; charset=utf-8")
		inputText, inputImage := r.request.InputTokens()
		message := gin.H{"role": "assistant", "content": r.output.String()}
		if len(annotations) > 0 {
			message["annotations"] = annotations
		}
		data, err = json.Marshal(gin.H{"id": result.ID, "object": "chat.completion", "created": result.Created, "model": result.Model, "choices": []gin.H{{"index": 0, "message": message, "finish_reason": "stop"}}, "usage": usage.Chat(inputText, inputImage, usage.Text(r.output.String(), result.Model), 0)})
		if err != nil {
			return err
		}
	}
	if err := r.send(ctx, data); err != nil {
		return err
	}
	r.delivered = true
	return nil
}

func (r *textResponse) imageComplete(ctx context.Context, request images.Request, result images.Result) error {
	var content strings.Builder
	for i, out := range result.Data {
		if i > 0 {
			content.WriteString("\n\n")
		}
		url := out.URL
		if out.B64JSON != "" {
			data, _ := base64.StdEncoding.DecodeString(out.B64JSON)
			media := http.DetectContentType(data)
			url = "data:" + media + ";base64," + out.B64JSON
		}
		content.WriteString("![image_" + strconv.Itoa(i+1) + "](" + url + ")")
	}
	text := content.String()
	meta := chat.Result{ID: "chatcmpl-" + r.c.Writer.Header().Get("X-Request-ID"), Created: result.Created, Model: request.Model}
	if r.stream {
		if err := r.delta(ctx, meta, text); err != nil {
			return err
		}
		return r.complete(ctx, meta)
	}
	u := result.Usage
	chatUsage := map[string]any{"prompt_tokens": u["input_tokens"], "completion_tokens": u["output_tokens"], "total_tokens": u["total_tokens"], "prompt_tokens_details": u["input_tokens_details"], "completion_tokens_details": u["output_tokens_details"]}
	data, err := json.Marshal(gin.H{"id": meta.ID, "object": "chat.completion", "created": meta.Created, "model": meta.Model, "choices": []gin.H{{"index": 0, "message": gin.H{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": chatUsage})
	if err != nil {
		return err
	}
	r.c.Header("Content-Type", "application/json; charset=utf-8")
	if err = r.send(ctx, data); err == nil {
		r.delivered = true
	}
	return err
}
func (r *responseDelivery) send(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	if total, ok := ctx.Deadline(); ok && total.Before(deadline) {
		deadline = total
	}
	if err := r.controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(cancelled); _ = r.controller.SetWriteDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-cancelled
		}
	}()
	if _, err := r.c.Writer.Write(data); err != nil {
		return err
	}
	if err := r.controller.Flush(); err != nil {
		return err
	}
	return ctx.Err()
}
func chunk(result chat.Result, delta gin.H, finish any) gin.H {
	return gin.H{"id": result.ID, "object": "chat.completion.chunk", "created": result.Created, "model": result.Model, "choices": []gin.H{{"index": 0, "delta": delta, "finish_reason": finish}}}
}
func chatErrorBody(err chat.Error) gin.H {
	kind := "server_error"
	if err.Status == 400 {
		kind = "invalid_request_error"
	}
	if err.Status == 429 {
		kind = "rate_limit_error"
	}
	return gin.H{"type": kind, "message": err.Message, "code": err.Code, "param": nil}
}
func writeChatError(c *gin.Context, err chat.Error) {
	c.JSON(err.Status, gin.H{"error": chatErrorBody(err)})
	c.Abort()
}
