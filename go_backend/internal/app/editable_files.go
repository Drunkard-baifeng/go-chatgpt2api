package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"chatgpt2api/go_backend/internal/editablefiles"
	"github.com/gin-gonic/gin"
)

func editableFileError(c *gin.Context, e error) {
	switch {
	case errors.Is(e, editablefiles.ErrInput):
		writeError(c, 400, "invalid_request_error", editablefiles.ErrInput.Error())
	case errors.Is(e, editablefiles.ErrNotFound):
		writeError(c, 404, "not_found_error", editablefiles.ErrNotFound.Error())
	case errors.Is(e, editablefiles.ErrConflict):
		writeError(c, 409, "conflict_error", editablefiles.ErrConflict.Error())
	case errors.Is(e, editablefiles.ErrFull):
		writeError(c, 429, "rate_limit_error", editablefiles.ErrFull.Error())
	default:
		writeError(c, 503, "server_error", editablefiles.ErrStorage.Error())
	}
}

func registerEditableFileRoutes(router *gin.Engine, options Options) {
	router.GET("/v1/editable-file-tasks", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		owner := textSubject(c, options.Auth, false)
		if owner == nil {
			return
		}
		if options.EditableFiles == nil {
			editableFileError(c, editablefiles.ErrStorage)
			return
		}
		limit := 0
		if raw, ok := c.GetQuery("limit"); ok {
			var e error
			limit, e = strconv.Atoi(raw)
			if e != nil {
				editableFileError(c, editablefiles.ErrInput)
				return
			}
		}
		var ids []string
		if raw, ok := c.GetQuery("ids"); ok {
			ids = strings.Split(raw, ",")
		}
		page, e := options.EditableFiles.List(c.Request.Context(), *owner, ids, limit)
		if e != nil {
			editableFileError(c, e)
			return
		}
		c.JSON(200, page)
	})
	for _, route := range []struct{ path, kind string }{{"/v1/editable-file-tasks", ""}, {"/v1/ppt/generations", "ppt"}, {"/v1/psd/generations", "psd"}} {
		router.POST(route.path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			owner := textSubject(c, options.Auth, false)
			if owner == nil {
				return
			}
			if options.EditableFiles == nil {
				editableFileError(c, editablefiles.ErrStorage)
				return
			}
			row, e := options.EditableFiles.Submit(c.Request.Context(), *owner, func(ctx context.Context) (editablefiles.Request, error) {
				var req editablefiles.Request
				e := managementInput(c, ctx, editablefiles.MaxRequestBytes, func(context.Context) error {
					decoder := json.NewDecoder(c.Request.Body)
					decoder.DisallowUnknownFields()
					var raw json.RawMessage
					if decoder.Decode(&raw) != nil {
						return editablefiles.ErrInput
					}
					var extra any
					if decoder.Decode(&extra) != io.EOF {
						return editablefiles.ErrInput
					}
					var fields map[string]json.RawMessage
					if json.Unmarshal(raw, &fields) != nil || fields == nil {
						return editablefiles.ErrInput
					}
					for _, value := range fields {
						if string(value) == "null" {
							return editablefiles.ErrInput
						}
					}
					strict := json.NewDecoder(strings.NewReader(string(raw)))
					strict.DisallowUnknownFields()
					if strict.Decode(&req) != nil {
						return editablefiles.ErrInput
					}
					return nil
				})
				if e != nil {
					return req, editablefiles.ErrInput
				}
				if route.kind != "" {
					if req.Kind != "" && req.Kind != route.kind {
						return req, editablefiles.ErrInput
					}
					req.Kind = route.kind
				}
				return req, nil
			})
			if e != nil {
				editableFileError(c, e)
				return
			}
			c.JSON(200, row)
		})
	}
	router.DELETE("/v1/editable-file-tasks/:task_id", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		owner := textSubject(c, options.Auth, false)
		if owner == nil {
			return
		}
		if options.EditableFiles == nil {
			editableFileError(c, editablefiles.ErrStorage)
			return
		}
		id := c.Param("task_id")
		if e := options.EditableFiles.Delete(c.Request.Context(), *owner, id); e != nil {
			editableFileError(c, e)
			return
		}
		c.JSON(200, gin.H{"task_id": id, "deleted": true})
	})
	router.GET("/files/*file_path", func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		if options.EditableFiles == nil {
			editableFileError(c, editablefiles.ErrNotFound)
			return
		}
		relative := strings.TrimPrefix(c.Param("file_path"), "/")
		f, e := options.EditableFiles.Open(c.Request.Context(), relative)
		if e != nil {
			editableFileError(c, e)
			return
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			editableFileError(c, editablefiles.ErrNotFound)
			return
		}
		name := info.Name()
		media := "application/octet-stream"
		switch {
		case strings.HasSuffix(name, ".pptx"):
			media = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		case strings.HasSuffix(name, ".ppt"):
			media = "application/vnd.ms-powerpoint"
		case strings.HasSuffix(name, ".psd"):
			media = "image/vnd.adobe.photoshop"
		case strings.HasSuffix(name, ".zip"):
			media = "application/zip"
		}
		c.Header("Content-Type", media)
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), f)
	})
}
