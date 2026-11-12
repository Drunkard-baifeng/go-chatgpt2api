package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/images"
	"github.com/gin-gonic/gin"
)

func registerImageRoutes(router *gin.Engine, options Options) {
	for _, route := range []struct {
		path string
		edit bool
	}{{"/v1/images/generations", false}, {"/v1/images/edits", true}} {
		router.POST(route.path, func(c *gin.Context) {
			subject := textSubject(c, options.Auth, false)
			if subject == nil {
				return
			}
			response := newResponseDelivery(c)
			if options.Images == nil {
				writeImageError(c, images.PublicError(images.ErrUnavailable))
				return
			}
			defer response.controller.SetWriteDeadline(time.Time{})
			var parsed images.Request
			_, err := options.Images.Execute(c.Request.Context(), *subject, func(ctx context.Context) (images.Request, error) {
				deadline := time.Now().Add(30 * time.Second)
				if total, ok := ctx.Deadline(); ok && total.Before(deadline) {
					deadline = total
				}
				_ = response.controller.SetReadDeadline(deadline)
				defer response.controller.SetReadDeadline(time.Time{})
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, images.MaxRequestBytes)
				body := c.Request.Body
				done := make(chan struct{})
				stop := context.AfterFunc(ctx, func() { defer close(done); body.Close() })
				defer func() {
					if !stop() {
						<-done
					}
				}()
				req, e := images.ParseRequest(c.Request, route.edit)
				if e == nil {
					req.BaseURL = imageRequestBaseURL(c.Request)
					parsed = req
				}
				return req, e
			}, func(ctx context.Context, result images.Result) error {
				if parsed.Stream {
					for _, output := range result.Data {
						if output.B64JSON == "" {
							continue
						}
						prefix := "image_generation"
						if route.edit {
							prefix = "image_edit"
						}
						if e := sendImageEvent(response, ctx, gin.H{"type": prefix + ".completed", "b64_json": output.B64JSON, "usage": output.Usage}); e != nil {
							return e
						}
					}
					// Python emits no completion item for URL-only image streams.
					if !c.Writer.Written() {
						setImageStreamHeaders(c)
						if e := response.send(ctx, nil); e != nil {
							return e
						}
					}
					response.delivered = true
					return nil
				}
				data, e := json.Marshal(result)
				if e != nil {
					return e
				}
				if len(data) > images.MaxRequestBytes {
					return images.ErrOutput
				}
				c.Header("Content-Type", "application/json; charset=utf-8")
				if e = response.send(ctx, data); e != nil {
					return e
				}
				response.delivered = true
				return nil
			})
			if err == nil {
				return
			}
			if response.delivered {
				options.Logger.Error("图片调用终态保存失败", "request_id", c.Writer.Header().Get("X-Request-ID"), "code", images.PublicError(err).Code)
				return
			}
			if c.Request.Context().Err() != nil || c.Writer.Written() {
				return
			}
			writeImageError(c, images.PublicError(err))
		})
	}
	asset := func(c *gin.Context) {
		if options.Assets == nil {
			c.Status(404)
			return
		}
		path := strings.TrimPrefix(c.Param("image_path"), "/")
		f, err := options.Assets.OpenContext(c.Request.Context(), path)
		if err != nil {
			c.Status(404)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			c.Status(404)
			return
		}
		response := newResponseDelivery(c)
		_ = response.controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		defer response.controller.SetWriteDeadline(time.Time{})
		c.Header("X-Content-Type-Options", "nosniff")
		switch {
		case strings.HasSuffix(path, ".png"):
			c.Header("Content-Type", "image/png")
		case strings.HasSuffix(path, ".jpeg"):
			c.Header("Content-Type", "image/jpeg")
		case strings.HasSuffix(path, ".webp"):
			c.Header("Content-Type", "image/webp")
		case strings.HasSuffix(path, ".gif"):
			c.Header("Content-Type", "image/gif")
		case strings.HasSuffix(path, ".svg"):
			c.Header("Content-Type", "image/svg+xml")
		}
		c.Header("Cache-Control", "public, max-age=86400, immutable")
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
	}
	router.GET("/images/*image_path", asset)
	router.HEAD("/images/*image_path", asset)
}

func writeImageError(c *gin.Context, err images.Error) {
	c.JSON(err.Status, gin.H{"error": imageErrorBody(err)})
	c.Abort()
}

func imageErrorBody(err images.Error) gin.H {
	return gin.H{"message": err.Message, "type": err.ErrorType(), "code": err.Code, "param": nil}
}

func setImageStreamHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
}

func sendImageEvent(response *responseDelivery, ctx context.Context, payload gin.H) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > images.MaxRequestBytes {
		return images.ErrOutput
	}
	kind, _ := payload["type"].(string)
	setImageStreamHeaders(response.c)
	return response.send(ctx, append(append([]byte("event: "+kind+"\ndata: "), data...), []byte("\n\n")...))
}

// Only the actual HTTP origin participates; forwarding headers require a
// separately configured trusted proxy and never replace the request authority.
func imageRequestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	parsed, err := url.Parse(scheme + "://" + host)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(host, "\\\r\n\t ") {
		return ""
	}
	return parsed.String()
}
