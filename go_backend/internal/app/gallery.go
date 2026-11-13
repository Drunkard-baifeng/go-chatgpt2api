package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/images"
	"github.com/gin-gonic/gin"
)

func galleryQueryInt(c *gin.Context, key string, fallback, maximum int) (int, error) {
	raw, present := c.Request.URL.Query()[key]
	if !present {
		return fallback, nil
	}
	if len(raw) != 1 || raw[0] == "" {
		return 0, images.ErrGalleryInput
	}
	value, e := strconv.Atoi(raw[0])
	if e != nil || value < 1 || value > maximum {
		return 0, images.ErrGalleryInput
	}
	return value, nil
}

func registerGalleryRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler func(*gin.Context) (any, error)) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Assets == nil {
				galleryError(c, images.ErrAssets)
				return
			}
			result, err := handler(c)
			if err != nil {
				galleryError(c, err)
				return
			}
			if !c.Writer.Written() {
				c.JSON(200, result)
			}
		})
	}
	register("GET", "/api/images", func(c *gin.Context) (any, error) {
		page, e := galleryQueryInt(c, "page", 1, int(^uint(0)>>1))
		if e != nil {
			return nil, e
		}
		size, e := galleryQueryInt(c, "page_size", 24, 500)
		if e != nil {
			return nil, e
		}
		return options.Assets.List(c.Request.Context(), images.GalleryQuery{BaseURL: imageRequestBaseURL(c.Request, options.Config.TrustedProxies...), Page: page, PageSize: size, MediaType: c.Query("media_type"), Tag: c.Query("tag"), Search: c.Query("search"), StartDate: c.Query("start_date"), EndDate: c.Query("end_date")})
	})
	register("POST", "/api/images/delete", func(c *gin.Context) (any, error) {
		var input struct {
			Paths []string `json:"paths"`
			Start string   `json:"start_date"`
			End   string   `json:"end_date"`
			All   bool     `json:"all_matching"`
		}
		if !managementBody(c, &input) {
			return nil, images.ErrGalleryInput
		}
		// 日期即使未启用筛选也必须校验，不能把无效过滤器当作全量删除。
		query := images.GalleryQuery{Page: 1, PageSize: 500, StartDate: input.Start, EndDate: input.End}
		var paths []string
		if input.All || input.Start != "" || input.End != "" {
			page, e := options.Assets.List(c.Request.Context(), query)
			if e != nil {
				return nil, e
			}
			if !input.All {
				return nil, images.ErrGalleryInput
			}
			if len(input.Paths) > 0 || page.Total > 1000 {
				return nil, images.ErrGalleryInput
			}
			for _, item := range page.Items {
				paths = append(paths, item.Path)
			}
			if page.HasMore {
				query.Page = 2
				next, e := options.Assets.List(c.Request.Context(), query)
				if e != nil {
					return nil, e
				}
				if next.Total > 1000 {
					return nil, images.ErrGalleryInput
				}
				for _, item := range next.Items {
					paths = append(paths, item.Path)
				}
			}
			if len(paths) == 0 {
				return gin.H{"removed": 0}, nil
			}
		} else {
			paths = input.Paths
		}
		removed, e := options.Assets.Delete(c.Request.Context(), paths)
		return gin.H{"removed": removed}, e
	})
	register("POST", "/api/images/tags", func(c *gin.Context) (any, error) {
		var input struct {
			Path string   `json:"path"`
			Tags []string `json:"tags"`
		}
		if !managementBody(c, &input) || input.Tags == nil {
			return nil, images.ErrGalleryInput
		}
		tags, e := options.Assets.UpdateTags(c.Request.Context(), input.Path, input.Tags)
		return gin.H{"ok": true, "tags": tags}, e
	})
	register("GET", "/api/images/storage", func(c *gin.Context) (any, error) { return options.Assets.Storage(c.Request.Context()) })
	register("POST", "/api/images/retention-cleanup", func(c *gin.Context) (any, error) { return options.Assets.CleanupExpired(c.Request.Context()) })
	register("POST", "/api/images/storage/cleanup-to-target", func(c *gin.Context) (any, error) {
		values, present := c.Request.URL.Query()["target_free_mb"]
		if !present || len(values) != 1 {
			return nil, images.ErrGalleryInput
		}
		target, e := strconv.ParseInt(values[0], 10, 64)
		if e != nil {
			return nil, images.ErrGalleryInput
		}
		dryRun := false
		if values, present := c.Request.URL.Query()["dry_run"]; present {
			if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
				return nil, images.ErrGalleryInput
			}
			dryRun = values[0] == "true"
		}
		return options.Assets.CleanupToTarget(c.Request.Context(), target, dryRun)
	})
	register("POST", "/api/images/download", func(c *gin.Context) (any, error) {
		var input struct {
			Paths []string `json:"paths"`
		}
		if !managementBody(c, &input) {
			return nil, images.ErrGalleryInput
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		data, e := options.Assets.DownloadZip(ctx, input.Paths)
		if e != nil {
			return nil, e
		}
		response := newResponseDelivery(c)
		defer response.controller.SetWriteDeadline(time.Time{})
		c.Header("Content-Disposition", `attachment; filename="images.zip"`)
		c.Header("Content-Type", "application/zip")
		if e = response.send(ctx, data); e != nil {
			return nil, e
		}
		return nil, nil
	})
	single := func(c *gin.Context) { serveGalleryAsset(c, options, false, true) }
	router.GET("/api/images/download/*image_path", single)
	router.HEAD("/api/images/download/*image_path", single)
	thumbnail := func(c *gin.Context) { serveGalleryAsset(c, options, true, false) }
	router.GET("/image-thumbnails/*image_path", thumbnail)
	router.HEAD("/image-thumbnails/*image_path", thumbnail)
}

func serveGalleryAsset(c *gin.Context, options Options, thumbnail, download bool) {
	if download && textSubject(c, options.Auth, true) == nil {
		return
	}
	if options.Assets == nil {
		c.Status(404)
		return
	}
	path := strings.TrimPrefix(c.Param("image_path"), "/")
	open := options.Assets.OpenContext
	if thumbnail {
		open = options.Assets.OpenThumbnailContext
	}
	file, e := open(c.Request.Context(), path)
	if e != nil {
		c.Status(404)
		return
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil {
		c.Status(404)
		return
	}
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetWriteDeadline(time.Time{})
	done := make(chan struct{})
	stop := context.AfterFunc(c.Request.Context(), func() { defer close(done); _ = controller.SetWriteDeadline(time.Now()); _ = file.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	c.Header("X-Content-Type-Options", "nosniff")
	if thumbnail {
		if strings.HasSuffix(path, ".svg") {
			c.Header("Content-Type", "image/svg+xml")
		} else {
			c.Header("Content-Type", "image/jpeg")
		}
		c.Header("Cache-Control", "public, max-age=86400")
	} else {
		extension := path[strings.LastIndex(path, ".")+1:]
		c.Header("Content-Disposition", `attachment; filename="image.`+extension+`"`)
		c.Header("Cache-Control", "no-store")
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

func galleryError(c *gin.Context, err error) {
	if c.Writer.Written() {
		return
	}
	if errors.Is(err, images.ErrGalleryInput) {
		writeError(c, 400, "invalid_request_error", images.ErrGalleryInput.Error())
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(c, 408, "timeout_error", "图库操作已取消或超时")
		return
	}
	writeError(c, 503, "server_error", "图库存储暂不可用")
}
