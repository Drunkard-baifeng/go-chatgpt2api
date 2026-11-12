package app

import (
	"context"
	"errors"
	"net/http"

	"chatgpt2api/go_backend/internal/prompts"
	"github.com/gin-gonic/gin"
)

func registerPromptRoutes(router *gin.Engine, options Options) {
	register := func(path string, admin bool, handler func(*gin.Context) (prompts.View, error)) {
		method := http.MethodPost
		if !admin {
			method = http.MethodGet
		}
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, admin) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Prompts == nil {
				promptError(c, prompts.ErrStorage)
				return
			}
			view, err := handler(c)
			if err != nil {
				if !c.Writer.Written() {
					promptError(c, err)
				}
				return
			}
			c.JSON(http.StatusOK, view)
		})
	}
	register("/api/prompts", false, func(c *gin.Context) (prompts.View, error) { return options.Prompts.View(c.Request.Context()) })
	register("/api/admin/prompt-sources/refresh", true, func(c *gin.Context) (prompts.View, error) { return options.Prompts.Refresh(c.Request.Context(), "") })
	register("/api/admin/prompt-sources/:source_id/refresh", true, func(c *gin.Context) (prompts.View, error) {
		return options.Prompts.Refresh(c.Request.Context(), c.Param("source_id"))
	})
	register("/api/admin/prompt-sources/:source_id", true, func(c *gin.Context) (prompts.View, error) {
		var request *prompts.UpdateRequest
		if !controlBody(c, &request) || request == nil {
			return prompts.View{}, prompts.ErrInput
		}
		return options.Prompts.UpdateSource(c.Request.Context(), c.Param("source_id"), *request)
	})
}
func promptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, prompts.ErrNotFound):
		writeError(c, 404, "not_found_error", prompts.ErrNotFound.Error())
	case errors.Is(err, prompts.ErrInput):
		writeError(c, 400, "invalid_request_error", prompts.ErrInput.Error())
	case errors.Is(err, prompts.ErrBusy):
		writeError(c, 429, "rate_limit_error", prompts.ErrBusy.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(c, 408, "timeout_error", "提示词操作已取消或超时")
	default:
		writeError(c, 503, "server_error", prompts.ErrStorage.Error())
	}
}
