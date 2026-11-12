package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"chatgpt2api/go_backend/internal/remoteimports"
	"github.com/gin-gonic/gin"
)

func remoteImportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, remoteimports.ErrInput), errors.Is(err, remoteimports.ErrOAuth):
		writeError(c, 400, "invalid_request_error", err.Error())
	case errors.Is(err, remoteimports.ErrNotFound):
		writeError(c, 404, "not_found_error", remoteimports.ErrNotFound.Error())
	case errors.Is(err, remoteimports.ErrConflict):
		writeError(c, 409, "conflict_error", remoteimports.ErrConflict.Error())
	case errors.Is(err, remoteimports.ErrOAuthBusy):
		writeError(c, 429, "rate_limit_error", remoteimports.ErrOAuthBusy.Error())
	case errors.Is(err, remoteimports.ErrRemote):
		writeError(c, 502, "server_error", remoteimports.ErrRemote.Error())
	case errors.Is(err, remoteimports.ErrStorage):
		writeError(c, 503, "server_error", remoteimports.ErrStorage.Error())
	default:
		accountError(c, err)
	}
}
func remoteImportBody(c *gin.Context, value any) bool {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	data, e := io.ReadAll(c.Request.Body)
	if e != nil || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		remoteImportError(c, remoteimports.ErrInput)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if decoder.Decode(value) != nil {
		remoteImportError(c, remoteimports.ErrInput)
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		remoteImportError(c, remoteimports.ErrInput)
		return false
	}
	return true
}
func registerRemoteImportRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler gin.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			if options.RemoteImports == nil {
				remoteImportError(c, remoteimports.ErrStorage)
				return
			}
			handler(c)
		})
	}
	for _, source := range []struct{ provider, root, singular, plural, param string }{{"cpa", "/api/cpa/pools", "pool", "pools", "pool_id"}, {"sub2api", "/api/sub2api/servers", "server", "servers", "server_id"}} {
		register("GET", source.root, func(c *gin.Context) {
			items, e := options.RemoteImports.List(c.Request.Context(), source.provider)
			if e != nil {
				remoteImportError(c, e)
				return
			}
			c.JSON(200, gin.H{source.plural: items})
		})
		save := func(c *gin.Context) {
			updates := map[string]any{}
			if !remoteImportBody(c, &updates) {
				return
			}
			item, e := options.RemoteImports.Save(c.Request.Context(), source.provider, c.Param(source.param), updates)
			if e != nil {
				remoteImportError(c, e)
				return
			}
			items, e := options.RemoteImports.List(c.Request.Context(), source.provider)
			if e != nil {
				remoteImportError(c, e)
				return
			}
			c.JSON(200, gin.H{source.singular: item, source.plural: items})
		}
		register("POST", source.root, save)
		path := source.root + "/:" + source.param
		register("POST", path, save)
		register("DELETE", path, func(c *gin.Context) {
			e := options.RemoteImports.Delete(c.Request.Context(), source.provider, c.Param(source.param))
			if e != nil {
				remoteImportError(c, e)
				return
			}
			items, e := options.RemoteImports.List(c.Request.Context(), source.provider)
			if e != nil {
				remoteImportError(c, e)
				return
			}
			c.JSON(200, gin.H{source.plural: items})
		})
		register("GET", path+"/import", func(c *gin.Context) {
			job, e := options.RemoteImports.Job(c.Request.Context(), source.provider, c.Param(source.param))
			if e != nil {
				remoteImportError(c, e)
				return
			}
			c.JSON(200, gin.H{"import_job": job})
		})
		register("POST", path+"/import", func(c *gin.Context) {
			var req remoteimports.ImportRequest
			if !remoteImportBody(c, &req) {
				return
			}
			job, e := options.RemoteImports.Start(c.Request.Context(), source.provider, c.Param(source.param), req)
			if e != nil {
				remoteImportError(c, e)
				return
			}
			c.JSON(200, gin.H{"import_job": job})
		})
		kinds := []string{"files"}
		if source.provider == "sub2api" {
			kinds = []string{"groups", "accounts"}
		}
		for _, kind := range kinds {
			register("GET", path+"/"+kind, func(c *gin.Context) {
				var group *string
				if values, ok := c.Request.URL.Query()["group_id"]; ok && len(values) > 0 {
					value := values[0]
					group = &value
				}
				items, e := options.RemoteImports.ListRemote(c.Request.Context(), source.provider, c.Param(source.param), kind, group)
				if e != nil {
					remoteImportError(c, e)
					return
				}
				c.JSON(200, gin.H{source.param: c.Param(source.param), kind: items})
			})
		}
	}
	register("POST", "/api/accounts/oauth/start", func(c *gin.Context) {
		var req struct {
			Email string `json:"email_hint"`
		}
		if !remoteImportBody(c, &req) {
			return
		}
		result, e := options.RemoteImports.OAuthStart(c.Request.Context(), req.Email)
		if e != nil {
			remoteImportError(c, e)
			return
		}
		c.JSON(200, result)
	})
	register("POST", "/api/accounts/oauth/finish", func(c *gin.Context) {
		var req struct {
			SessionID string  `json:"session_id"`
			Callback  string  `json:"callback"`
			Target    *string `json:"target_group_id"`
		}
		if !remoteImportBody(c, &req) {
			return
		}
		result, e := options.RemoteImports.OAuthFinish(c.Request.Context(), req.SessionID, req.Callback, req.Target)
		if e != nil {
			remoteImportError(c, e)
			return
		}
		c.JSON(200, result)
	})
}
