package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
	"github.com/gin-gonic/gin"
)

func observabilityError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, callrecord.ErrInput):
		writeError(c, 400, "invalid_request_error", callrecord.ErrInput.Error())
	case errors.Is(err, callrecord.ErrNotFound):
		writeError(c, 404, "not_found_error", callrecord.ErrNotFound.Error())
	case errors.Is(err, context.Canceled):
		writeError(c, 499, "server_error", "操作已取消")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "server_error", "操作超时")
	default:
		writeError(c, 503, "server_error", callrecord.ErrStorage.Error())
	}
}
func registerObservabilityRoutes(router *gin.Engine, options Options) {
	imageBaseURL := func(c *gin.Context) (string, error) {
		if options.Assets != nil {
			return options.Assets.ResolvePublicBaseURL(c.Request.Context(), imageRequestBaseURL(c.Request))
		}
		return imageRequestBaseURL(c.Request), nil
	}
	register := func(method, path string, handler gin.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Observability == nil {
				observabilityError(c, callrecord.ErrStorage)
				return
			}
			handler(c)
		})
	}
	register("GET", "/api/logs", func(c *gin.Context) {
		if len(c.Request.URL.RawQuery) > 8192 {
			observabilityError(c, callrecord.ErrInput)
			return
		}
		limit, offset := 200, 0
		var err error
		if raw, exists := c.GetQuery("limit"); exists {
			limit, err = strconv.Atoi(raw)
			if err != nil {
				observabilityError(c, callrecord.ErrInput)
				return
			}
		}
		if raw, exists := c.GetQuery("offset"); exists {
			offset, err = strconv.Atoi(raw)
			if err != nil {
				observabilityError(c, callrecord.ErrInput)
				return
			}
		}
		filter := callrecord.Filter{Type: c.Query("type"), StartDate: c.Query("start_date"), EndDate: c.Query("end_date"), Status: c.Query("status"), Endpoint: c.Query("endpoint"), Model: c.Query("model"), Account: c.Query("account"), ConversationID: c.Query("conversation_id"), Search: c.Query("search"), Limit: limit, Offset: offset}
		baseURL, err := imageBaseURL(c)
		if err != nil {
			observabilityError(c, err)
			return
		}
		view, err := options.Observability.List(c.Request.Context(), filter, baseURL)
		if err != nil {
			observabilityError(c, err)
			return
		}
		c.JSON(200, view)
	})
	register("GET", "/api/logs/:log_id", func(c *gin.Context) {
		baseURL, err := imageBaseURL(c)
		if err != nil {
			observabilityError(c, err)
			return
		}
		view, err := options.Observability.Detail(c.Request.Context(), c.Param("log_id"), baseURL)
		if err != nil {
			observabilityError(c, err)
			return
		}
		c.JSON(200, view)
	})
	register("POST", "/api/logs/delete", func(c *gin.Context) {
		controller := http.NewResponseController(c.Writer)
		_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
		var body struct {
			IDs []string `json:"ids"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			observabilityError(c, callrecord.ErrInput)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			observabilityError(c, callrecord.ErrInput)
			return
		}
		view, err := options.Observability.Delete(c.Request.Context(), body.IDs)
		if err != nil {
			observabilityError(c, err)
			return
		}
		c.JSON(200, view)
	})
	register("GET", "/api/monitor/realtime", func(c *gin.Context) { c.JSON(200, options.Observability.Monitor()) })
	register("GET", "/api/monitor/realtime/:call_id", func(c *gin.Context) {
		baseURL, err := imageBaseURL(c)
		if err != nil {
			observabilityError(c, err)
			return
		}
		view, err := options.Observability.MonitorDetail(c.Param("call_id"), baseURL)
		if err != nil {
			observabilityError(c, err)
			return
		}
		c.JSON(200, view)
	})
	register("GET", "/api/dashboard", func(c *gin.Context) {
		view, err := options.Observability.Dashboard(c.Request.Context())
		if err != nil {
			observabilityError(c, err)
			return
		}
		c.JSON(200, view)
	})
}
