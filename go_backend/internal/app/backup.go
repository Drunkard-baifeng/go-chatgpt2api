package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"chatgpt2api/go_backend/internal/backup"
	"github.com/gin-gonic/gin"
)

func registerBackupRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler func(*gin.Context) (any, error)) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Backup == nil {
				backupError(c, backup.ErrStorage)
				return
			}
			result, err := handler(c)
			if err != nil {
				backupError(c, err)
				return
			}
			c.JSON(http.StatusOK, result)
		})
	}
	register("POST", "/api/backup/test", func(c *gin.Context) (any, error) {
		result, err := options.Backup.Test(c.Request.Context())
		return gin.H{"result": result}, err
	})
	register("GET", "/api/backups", func(c *gin.Context) (any, error) { return options.Backup.List(c.Request.Context()) })
	register("POST", "/api/backups/run", func(c *gin.Context) (any, error) {
		result, err := options.Backup.Run(c.Request.Context(), "manual")
		return gin.H{"result": result}, err
	})
	register("POST", "/api/backups/delete", func(c *gin.Context) (any, error) {
		var request *struct {
			Key string `json:"key"`
		}
		if !controlBody(c, &request) || request == nil || strings.TrimSpace(request.Key) == "" || len(request.Key) > 1024 {
			return nil, backup.ErrInput
		}
		err := options.Backup.Delete(c.Request.Context(), request.Key)
		return gin.H{"ok": true}, err
	})
}
func backupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, backup.ErrBusy):
		writeError(c, 409, "conflict_error", backup.ErrBusy.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(c, 408, "timeout_error", "备份操作已取消或超时")
	case errors.Is(err, backup.ErrStorage), errors.Is(err, backup.ErrClosed):
		writeError(c, 503, "server_error", backup.ErrStorage.Error())
	default:
		message := "备份执行失败"
		for _, known := range []error{backup.ErrInput, backup.ErrRemote, backup.ErrLimit, backup.ErrSnapshot, backup.ErrDumpMissing, backup.ErrDump} {
			if errors.Is(err, known) {
				message = known.Error()
				break
			}
		}
		writeError(c, 400, "invalid_request_error", message)
	}
}
