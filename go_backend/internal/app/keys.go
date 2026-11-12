package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"chatgpt2api/go_backend/internal/auth"
	"github.com/gin-gonic/gin"
)

func registerKeyRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler gin.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			if options.Keys == nil {
				keyError(c, auth.ErrKeyStorage)
				return
			}
			handler(c)
		})
	}
	register(http.MethodGet, "/api/auth/users", func(c *gin.Context) {
		items, err := options.Keys.List(c.Request.Context())
		if err != nil {
			keyError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	register(http.MethodPost, "/api/auth/users", func(c *gin.Context) {
		var request auth.KeyCreate
		if !keyBody(c, &request) {
			return
		}
		item, raw, err := options.Keys.Create(c.Request.Context(), request)
		if err != nil {
			keyError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"item": item, "raw_key": raw})
	})
	register(http.MethodPost, "/api/auth/users/:key_id", func(c *gin.Context) {
		var request auth.KeyUpdate
		if !keyBody(c, &request) {
			return
		}
		item, err := options.Keys.Update(c.Request.Context(), c.Param("key_id"), request)
		if err != nil {
			keyError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"item": item})
	})
	register(http.MethodDelete, "/api/auth/users/:key_id", func(c *gin.Context) {
		if err := options.Keys.Delete(c.Request.Context(), c.Param("key_id")); err != nil {
			keyError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted_id": c.Param("key_id")})
	})
}

func keyBody(c *gin.Context, value any) bool {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	// 仅接受对象；JSON null 不能被当成空创建请求。
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '{' {
		keyError(c, auth.ErrKeyInput)
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		keyError(c, auth.ErrKeyInput)
		return false
	}
	if _, creating := value.(*auth.KeyCreate); creating {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if bytes.Equal(bytes.TrimSpace(fields["name"]), []byte("null")) {
			keyError(c, auth.ErrKeyInput)
			return false
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if strict.Decode(value) != nil {
		keyError(c, auth.ErrKeyInput)
		return false
	}
	return true
}

func keyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrKeyInput):
		writeError(c, http.StatusBadRequest, "invalid_request_error", auth.ErrKeyInput.Error())
	case errors.Is(err, auth.ErrKeyNotFound):
		writeError(c, http.StatusNotFound, "not_found_error", auth.ErrKeyNotFound.Error())
	case errors.Is(err, auth.ErrKeyConflict):
		writeError(c, http.StatusConflict, "conflict_error", auth.ErrKeyConflict.Error())
	case errors.Is(err, context.Canceled):
		writeError(c, 499, "server_error", "用户密钥操作已取消")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "server_error", "用户密钥操作超时")
	default:
		writeError(c, http.StatusServiceUnavailable, "server_error", auth.ErrKeyStorage.Error())
	}
}
