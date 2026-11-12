// Package app 只负责 HTTP 适配，业务代码使用标准 context。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/backup"
	"chatgpt2api/go_backend/internal/chat"
	"chatgpt2api/go_backend/internal/cleanup"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/editablefiles"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/imagetasks"
	"chatgpt2api/go_backend/internal/observability"
	"chatgpt2api/go_backend/internal/prompts"
	"chatgpt2api/go_backend/internal/proxy"
	"chatgpt2api/go_backend/internal/remoteimports"
	"chatgpt2api/go_backend/internal/settings"
	"chatgpt2api/go_backend/internal/updates"
	"github.com/gin-gonic/gin"
)

type Options struct {
	Accounts      *accounts.Repository
	Text          *chat.Service
	Images        *images.Service
	Tasks         *imagetasks.Service
	Settings      *settings.Service
	Proxy         *proxy.Service
	Prompts       *prompts.Service
	Observability *observability.Service
	RemoteImports *remoteimports.Service
	Backup        *backup.Service
	Cleanup       *cleanup.Service
	Updates       *updates.Service
	EditableFiles *editablefiles.Service
	Keys          *auth.Keys
	Assets        *images.ImageStorageService
	Auth          *auth.Service
	Version       string
	Config        config.Server
	Logger        *slog.Logger
	Ready         func(context.Context) error
}

func NewRouter(options Options) (*gin.Engine, error) {
	if options.Auth == nil || options.Logger == nil || options.Ready == nil {
		return nil, errors.New("HTTP 服务缺少必要依赖")
	}
	router := gin.New()
	if err := router.SetTrustedProxies(options.Config.TrustedProxies); err != nil {
		return nil, errors.New("可信代理配置无效")
	}
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.Use(observe(options.Logger), cors(options.Config.AllowedOrigins))
	router.GET("/version", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"version": options.Version}) })
	router.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := options.Ready(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	login := func(c *gin.Context) {
		subject, err := options.Auth.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) && c.Request.Method == http.MethodGet {
				c.JSON(http.StatusOK, auth.BuildView(options.Version, nil))
				return
			}
			authError(c, err)
			return
		}
		c.JSON(http.StatusOK, auth.BuildView(options.Version, subject))
	}
	router.GET("/auth/status", login)
	router.POST("/auth/login", login)
	registerTextRoutes(router, options)
	registerProtocolRoutes(router, options)
	registerImageRoutes(router, options)
	registerImageTaskRoutes(router, options)
	registerGalleryRoutes(router, options)
	registerAccountRoutes(router, options)
	registerAccountProbe(router, options)
	registerControlRoutes(router, options)
	registerKeyRoutes(router, options)
	registerPromptRoutes(router, options)
	registerObservabilityRoutes(router, options)
	registerRemoteImportRoutes(router, options)
	registerBackupRoutes(router, options)
	registerCleanupRoutes(router, options)
	registerUpdateRoutes(router, options)
	registerEditableFileRoutes(router, options)
	registerImageStorageRoutes(router, options)
	router.NoRoute(func(c *gin.Context) { writeError(c, http.StatusNotFound, "not_found_error", "接口不存在") })
	return router, nil
}

// 跨域响应只发放给显式来源；不干预同源或非浏览器请求的业务鉴权。
// 不使用 Cookie 登录，也不开放凭据型跨域；预检不需要 Bearer。
func cors(origins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		allowed[origin] = true
	}
	const methods = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS"
	const headers = "Authorization, Content-Type, X-Api-Key, Anthropic-Version, Anthropic-Beta"
	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Origin")
		origin := c.GetHeader("Origin")
		preflight := c.Request.Method == http.MethodOptions && origin != "" && c.GetHeader("Access-Control-Request-Method") != ""
		if preflight {
			c.Writer.Header().Add("Vary", "Access-Control-Request-Method")
			c.Writer.Header().Add("Vary", "Access-Control-Request-Headers")
			valid := allowed[origin] && containsToken(methods, c.GetHeader("Access-Control-Request-Method"))
			for _, header := range strings.Split(c.GetHeader("Access-Control-Request-Headers"), ",") {
				if strings.TrimSpace(header) != "" && !containsToken(headers, header) {
					valid = false
				}
			}
			if !valid {
				writeError(c, http.StatusForbidden, "permission_error", "跨域来源、方法或请求头未获允许")
				return
			}
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", methods)
			c.Header("Access-Control-Allow-Headers", headers)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		if allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Expose-Headers", "X-Request-ID, Content-Disposition, X-Export-Requested, X-Exported, X-Skipped, WWW-Authenticate")
		}
		c.Next()
	}
}

func containsToken(list, token string) bool {
	for _, value := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(token)) {
			return true
		}
	}
	return false
}

func authError(c *gin.Context, err error) {
	if errors.Is(err, auth.ErrUnauthorized) {
		c.Header("WWW-Authenticate", "Bearer")
		writeError(c, http.StatusUnauthorized, "authentication_error", auth.ErrUnauthorized.Error())
		return
	}
	writeError(c, http.StatusServiceUnavailable, "server_error", auth.ErrUnavailable.Error())
}

func writeError(c *gin.Context, status int, kind, message string) {
	path := c.Request.URL.Path
	if path == "/v1/messages" {
		if kind == "server_error" {
			kind = "api_error"
		}
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
	} else if strings.HasPrefix(path, "/v1/") {
		c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message, "param": nil, "code": nil}})
	} else {
		c.JSON(status, gin.H{"detail": gin.H{"error": message}})
	}
	c.Abort()
}

// 仅记录路由模板和状态；不读取请求体、密钥、URL 查询或 panic 值。
// 保留 Gin 原生 ResponseWriter，SSE 可以直接 Flush。
func observe(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			writeError(c, 500, "server_error", "请求初始化失败")
			return
		}
		requestID := hex.EncodeToString(id[:])
		c.Header("X-Request-ID", requestID)
		started := time.Now()
		defer func() {
			if recover() != nil {
				logger.Error("HTTP 处理异常", "request_id", requestID, "route", c.FullPath())
				if !c.Writer.Written() {
					writeError(c, 500, "server_error", "服务内部错误")
				} else {
					c.Abort()
				}
			}
			logger.Info("HTTP 请求", "request_id", requestID, "method", c.Request.Method, "route", c.FullPath(), "status", c.Writer.Status(), "duration_ms", time.Since(started).Milliseconds())
		}()
		c.Next()
	}
}
