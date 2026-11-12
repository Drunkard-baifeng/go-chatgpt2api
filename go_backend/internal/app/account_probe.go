package app

import (
	"bytes"
	"chatgpt2api/go_backend/internal/chat"
	"chatgpt2api/go_backend/internal/images"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

// 管理员账号测试复用公开生成链的准入、凭据、Call Record 与计数。
func registerAccountProbe(router *gin.Engine, options Options) {
	router.POST("/api/accounts/:account_id/test", func(c *gin.Context) {
		subject := textSubject(c, options.Auth, true)
		if subject == nil {
			return
		}
		c.Header("Cache-Control", "no-store")
		if options.Accounts == nil {
			writeError(c, 503, "server_error", "账号仓储暂不可用")
			return
		}
		var input struct {
			Mode   string `json:"mode"`
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if !accountBody(c, &input) {
			return
		}
		if (input.Mode != "chat" && input.Mode != "image") || strings.TrimSpace(input.Model) == "" || len(input.Prompt) > 20000 || strings.TrimSpace(input.Prompt) == "" {
			writeError(c, 400, "invalid_request_error", "测试模式、模型或提示词无效")
			return
		}
		id := c.Param("account_id")
		before, err := options.Accounts.Detail(c.Request.Context(), id)
		if err != nil {
			accountError(c, err)
			return
		}
		started := time.Now()
		modeLabel := "文本测试"
		var content strings.Builder
		code, message := "", ""
		if input.Mode == "chat" {
			if options.Text == nil {
				writeError(c, 503, "server_error", "文本服务暂不可用")
				return
			}
			raw, _ := json.Marshal(map[string]any{"model": input.Model, "prompt": input.Prompt})
			req, e := chat.ParseRequest(bytes.NewReader(raw))
			if e != nil {
				writeChatError(c, chat.PublicError(e))
				return
			}
			_, err = options.Text.ExecuteForAccount(c.Request.Context(), *subject, req, id, func(ctx context.Context, _ chat.Result, delta string) error {
				if content.Len()+len(delta) > chat.MaxRequestBytes {
					return chat.ErrDelivery
				}
				content.WriteString(delta)
				return ctx.Err()
			}, nil)
			if err != nil {
				public := chat.PublicError(err)
				code, message = public.Code, public.Message
			}
		} else {
			modeLabel = "图片测试"
			if options.Images == nil {
				writeError(c, 503, "server_error", "图片服务暂不可用")
				return
			}
			raw, _ := json.Marshal(map[string]any{"model": input.Model, "prompt": input.Prompt, "n": 1, "response_format": "url"})
			_, err = options.Images.ExecuteForAccount(c.Request.Context(), *subject, id, func(ctx context.Context) (images.Request, error) {
				req, _ := http.NewRequestWithContext(ctx, "POST", "/v1/images/generations", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				return images.ParseRequest(req, false)
			}, func(ctx context.Context, result images.Result) error {
				if len(result.Data) > 0 {
					content.WriteString(result.Data[0].URL)
				}
				return ctx.Err()
			})
			if err != nil {
				public := images.PublicError(err)
				code, message = public.Code, public.Message
			}
		}
		after := before
		if snapshot, e := options.Accounts.Detail(c.Request.Context(), id); e == nil {
			after = snapshot
		}
		status, label, tone := "success", "测试成功", "success"
		if err != nil {
			status, label, tone = "failed", "测试失败", "danger"
			content.Reset()
		}
		deducted := false
		b, bok := before["quota_remaining"].(int)
		a, aok := after["quota_remaining"].(int)
		if bok && aok {
			deducted = a < b
		}
		c.JSON(200, gin.H{"status": status, "status_label": label, "tone": tone, "account_id": id, "account_label": before["display_name"], "mode": input.Mode, "mode_label": modeLabel, "model": input.Model, "duration_ms": time.Since(started).Milliseconds(), "content": content.String(), "quota_before_label": before["quota_label"], "quota_after_label": after["quota_label"], "quota_deducted": deducted, "error_code": code, "error_message": message})
	})
}
