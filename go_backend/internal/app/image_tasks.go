package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/imagetasks"
	"github.com/gin-gonic/gin"
)

// 输入预算只覆盖读取；持久化后的任务生命周期由任务服务拥有。
func managementInput(c *gin.Context, parent context.Context, limit int64, read func(context.Context) error) error {
	if c.Request.ContentLength > limit {
		return images.ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	controller := http.NewResponseController(c.Writer)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	defer controller.SetReadDeadline(time.Time{})
	original := c.Request
	defer func() { c.Request = original }()
	c.Request = c.Request.WithContext(ctx)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	body := c.Request.Body
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = body.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	return read(ctx)
}

func managementBody(c *gin.Context, destination any) bool {
	return managementInput(c, c.Request.Context(), 2<<20, func(context.Context) error {
		decoder := json.NewDecoder(c.Request.Body)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return imagetasks.ErrInput
		}
		if data := bytes.TrimSpace(raw); len(data) == 0 || data[0] != '{' {
			return imagetasks.ErrInput
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return imagetasks.ErrInput
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return imagetasks.ErrInput
			}
		}
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		return strict.Decode(destination)
	}) == nil
}

func registerImageTaskRoutes(router *gin.Engine, options Options) {
	router.GET("/api/image-tasks", func(c *gin.Context) {
		subject := textSubject(c, options.Auth, false)
		if subject == nil {
			return
		}
		c.Header("Cache-Control", "no-store")
		if options.Tasks == nil {
			writeTaskError(c, imagetasks.ErrStorage)
			return
		}
		var ids []string
		if raw, ok := c.GetQuery("ids"); ok {
			ids = strings.Split(raw, ",")
		}
		page, err := options.Tasks.List(c.Request.Context(), *subject, ids)
		if err != nil {
			writeTaskError(c, err)
			return
		}
		c.JSON(200, page)
	})
	for _, route := range []struct {
		path string
		edit bool
	}{{"/api/image-tasks/generations", false}, {"/api/image-tasks/edits", true}} {
		router.POST(route.path, func(c *gin.Context) {
			subject := textSubject(c, options.Auth, false)
			if subject == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Tasks == nil {
				writeTaskError(c, imagetasks.ErrStorage)
				return
			}
			row, err := options.Tasks.Submit(c.Request.Context(), *subject, func(ctx context.Context) (id string, request images.Request, err error) {
				err = managementInput(c, ctx, images.MaxRequestBytes, func(context.Context) error {
					var parseErr error
					id, request, parseErr = images.ParseTaskRequest(c.Request, route.edit)
					if parseErr == nil {
						request.BaseURL = imageRequestBaseURL(c.Request, options.Config.TrustedProxies...)
					}
					return parseErr
				})
				return
			})
			if err != nil {
				writeTaskError(c, err)
				return
			}
			c.JSON(200, row)
		})
	}
	router.POST("/api/image-tasks/:task_id/resume-poll", func(c *gin.Context) {
		subject := textSubject(c, options.Auth, false)
		if subject == nil {
			return
		}
		c.Header("Cache-Control", "no-store")
		if options.Tasks == nil {
			writeTaskError(c, imagetasks.ErrStorage)
			return
		}
		extra := 30.0
		input := struct {
			Extra *float64 `json:"extra_timeout_secs"`
		}{Extra: &extra}
		if !managementBody(c, &input) || input.Extra == nil || math.IsNaN(*input.Extra) || math.IsInf(*input.Extra, 0) || *input.Extra < 5 || *input.Extra > 120 {
			writeTaskError(c, imagetasks.ErrInput)
			return
		}
		row, err := options.Tasks.Resume(c.Request.Context(), *subject, c.Param("task_id"), time.Duration(*input.Extra*float64(time.Second)))
		if err != nil {
			writeTaskError(c, err)
			return
		}
		c.JSON(200, row)
	})
}

func writeTaskError(c *gin.Context, err error) {
	status, code, message := 503, "image_task_storage_unavailable", imagetasks.ErrStorage.Error()
	switch {
	case errors.Is(err, imagetasks.ErrFull):
		code, message = "image_task_queue_full", imagetasks.ErrFull.Error()
	case errors.Is(err, imagetasks.ErrInput):
		status, code, message = 400, "invalid_image_task", imagetasks.ErrInput.Error()
	case errors.Is(err, imagetasks.ErrNotFound):
		status, code, message = 404, "image_task_not_found", imagetasks.ErrNotFound.Error()
	case errors.Is(err, imagetasks.ErrStorage):
	default:
		public := images.PublicError(err)
		status, code, message = public.Status, public.Code, public.Message
	}
	c.AbortWithStatusJSON(status, gin.H{"detail": gin.H{"error": message, "code": code}})
}
