package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"chatgpt2api/go_backend/internal/cleanup"
	"github.com/gin-gonic/gin"
)

func cleanupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cleanup.ErrInput):
		writeError(c, 400, "invalid_request_error", cleanup.ErrInput.Error())
	case errors.Is(err, cleanup.ErrBusy):
		writeError(c, 409, "conflict_error", cleanup.ErrBusy.Error())
	case errors.Is(err, context.Canceled):
		writeError(c, 499, "server_error", "清理已取消")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "server_error", "清理超时")
	default:
		writeError(c, 503, "server_error", cleanup.ErrStorage.Error())
	}
}
func cleanupBody(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil {
		return false
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}
func registerCleanupRoutes(router *gin.Engine, options Options) {
	for _, kind := range []string{"retention-cleanup", "account-cleanup"} {
		for _, action := range []string{"preview", "run"} {
			router.POST("/api/settings/"+kind+"/"+action, func(c *gin.Context) {
				if textSubject(c, options.Auth, true) == nil {
					return
				}
				c.Header("Cache-Control", "no-store")
				if options.Cleanup == nil {
					cleanupError(c, cleanup.ErrStorage)
					return
				}
				var result any
				var err error
				if kind == "retention-cleanup" {
					var request cleanup.RetentionRequest
					if !cleanupBody(c, &request) {
						cleanupError(c, cleanup.ErrInput)
						return
					}
					result, err = options.Cleanup.Retention(c.Request.Context(), request, action == "preview")
				} else {
					var request cleanup.AccountRequest
					if !cleanupBody(c, &request) {
						cleanupError(c, cleanup.ErrInput)
						return
					}
					result, err = options.Cleanup.Accounts(c.Request.Context(), request, action == "preview")
				}
				if err != nil {
					cleanupError(c, err)
					return
				}
				c.JSON(200, result)
			})
		}
	}
}
