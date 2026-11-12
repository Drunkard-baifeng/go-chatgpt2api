package app

import (
	"context"
	"errors"

	"chatgpt2api/go_backend/internal/images"
	"github.com/gin-gonic/gin"
)

func registerImageStorageRoutes(router *gin.Engine, options Options) {
	register := func(path string, body bool, handler func(*gin.Context) (any, error)) {
		router.POST(path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Assets == nil {
				imageStorageError(c, images.ErrAssets)
				return
			}
			if !body && c.Request.ContentLength != 0 {
				var empty struct{}
				if !managementBody(c, &empty) {
					imageStorageError(c, images.ErrGalleryInput)
					return
				}
			}
			result, e := handler(c)
			if e != nil {
				imageStorageError(c, e)
				return
			}
			c.JSON(200, result)
		})
	}
	register("/api/image-storage/test", false, func(c *gin.Context) (any, error) {
		result, e := options.Assets.TestWebDAV(c.Request.Context())
		return gin.H{"result": result}, e
	})
	register("/api/image-storage/sync", false, func(c *gin.Context) (any, error) {
		result, e := options.Assets.SyncWebDAV(c.Request.Context())
		return gin.H{"result": result}, e
	})
	register("/api/images/storage/compress", false, func(c *gin.Context) (any, error) { return options.Assets.Compress(c.Request.Context()) })
	register("/api/images/genbox-push", true, func(c *gin.Context) (any, error) {
		var input struct {
			Path string `json:"path"`
		}
		if !managementBody(c, &input) {
			return nil, images.ErrGalleryInput
		}
		return options.Assets.PushGenBox(c.Request.Context(), input.Path, nil)
	})
}

func imageStorageError(c *gin.Context, e error) {
	var genbox images.GenBoxError
	if errors.As(e, &genbox) {
		c.JSON(genbox.Status, gin.H{"error": gin.H{"message": genbox.Message, "type": "server_error", "code": genbox.Code, "param": nil}})
		c.Abort()
		return
	}
	switch {
	case errors.Is(e, images.ErrGalleryInput):
		writeError(c, 400, "invalid_request_error", images.ErrGalleryInput.Error())
	case errors.Is(e, images.ErrStorageConfig):
		writeError(c, 409, "conflict_error", images.ErrStorageConfig.Error())
	case errors.Is(e, images.ErrStorageBusy):
		writeError(c, 429, "rate_limit_error", images.ErrStorageBusy.Error())
	case errors.Is(e, images.ErrStorageRemote):
		writeError(c, 502, "upstream_error", images.ErrStorageRemote.Error())
	case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		writeError(c, 408, "timeout_error", "图片存储操作已取消或超时")
	default:
		writeError(c, 503, "server_error", images.ErrAssets.Error())
	}
}
