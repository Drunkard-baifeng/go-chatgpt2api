package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"chatgpt2api/go_backend/internal/proxy"
	"chatgpt2api/go_backend/internal/settings"
	"github.com/gin-gonic/gin"
)

func registerControlRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, admin bool, handler func(*gin.Context) (any, error)) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, admin) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			result, err := handler(c)
			if err != nil {
				controlError(c, err)
				return
			}
			c.JSON(http.StatusOK, result)
		})
	}
	register("GET", "/api/settings", true, func(c *gin.Context) (any, error) {
		if options.Settings == nil {
			return nil, settings.ErrStorage
		}
		return options.Settings.View(c.Request.Context())
	})
	register("GET", "/api/third-party-apps", false, func(c *gin.Context) (any, error) {
		if options.Settings == nil {
			return nil, settings.ErrStorage
		}
		return options.Settings.ThirdPartyApps(c.Request.Context())
	})
	register("PATCH", "/api/settings", true, func(c *gin.Context) (any, error) {
		if options.Settings == nil {
			return nil, settings.ErrStorage
		}
		var input map[string]any
		if !controlBody(c, &input) {
			return nil, settings.ErrInput
		}
		return options.Settings.Patch(c.Request.Context(), input)
	})
	register("GET", "/api/proxy/view", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		return options.Proxy.View(c.Request.Context())
	})
	register("POST", "/api/proxy/defaults", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input proxy.DefaultsRequest
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		return options.Proxy.SaveDefaults(c.Request.Context(), input)
	})
	register("POST", "/api/proxy/groups", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input map[string]any
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		return options.Proxy.SaveGroup(c.Request.Context(), input)
	})
	register("DELETE", "/api/proxy/groups/:group_id", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		return options.Proxy.DeleteGroup(c.Request.Context(), c.Param("group_id"))
	})
	register("POST", "/api/proxy/nodes/import", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input struct {
			Text         string   `json:"text"`
			ExistingURLs []string `json:"existing_urls"`
		}
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		return options.Proxy.ImportNodes(input.Text, input.ExistingURLs)
	})
	register("POST", "/api/proxy/test", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input struct {
			URL string `json:"url"`
		}
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		result, err := options.Proxy.TestActive(c.Request.Context(), input.URL)
		return gin.H{"result": result}, err
	})
	register("POST", "/api/proxy/groups/test", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input struct {
			ID     string `json:"id"`
			NodeID string `json:"node_id"`
			URL    string `json:"url"`
		}
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		return options.Proxy.TestGroup(c.Request.Context(), input.ID, input.NodeID, input.URL)
	})
	runtime := func(c *gin.Context) (map[string]any, error) {
		if options.Settings == nil {
			return nil, settings.ErrStorage
		}
		view, err := options.Settings.View(c.Request.Context())
		if err != nil {
			return nil, err
		}
		return view["settings"].(map[string]any)["proxy_runtime"].(map[string]any), nil
	}
	register("GET", "/api/proxy/runtime", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		values, err := runtime(c)
		if err != nil {
			return nil, err
		}
		return options.Proxy.Runtime(c.Request.Context(), values)
	})
	register("POST", "/api/proxy/clearance/test", true, func(c *gin.Context) (any, error) {
		if options.Proxy == nil {
			return nil, proxy.ErrStorage
		}
		var input struct {
			TargetURL string `json:"target_url"`
		}
		if !controlBody(c, &input) {
			return nil, proxy.ErrInput
		}
		values, err := runtime(c)
		if err != nil {
			return nil, err
		}
		result, err := options.Proxy.TestClearance(c.Request.Context(), input.TargetURL, values)
		return gin.H{"result": result}, err
	})
}

func controlBody(c *gin.Context, destination any) bool {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
func controlError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, settings.ErrConflict), errors.Is(err, proxy.ErrInUse):
		writeError(c, 409, "conflict_error", err.Error())
	case errors.Is(err, settings.ErrInput), errors.Is(err, proxy.ErrInput), errors.Is(err, proxy.ErrUnavailable):
		writeError(c, 400, "invalid_request_error", err.Error())
	case errors.Is(err, proxy.ErrNotFound):
		writeError(c, 404, "not_found_error", proxy.ErrNotFound.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(c, 408, "timeout_error", "操作已取消或超时")
	default:
		writeError(c, 503, "server_error", "设置或代理服务暂不可用")
	}
}
