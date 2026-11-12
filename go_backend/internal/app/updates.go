package app

import (
	"github.com/gin-gonic/gin"
	"strconv"
)

func registerUpdateRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler gin.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Updates == nil {
				writeError(c, 503, "server_error", "更新状态暂不可用")
				return
			}
			handler(c)
		})
	}
	register("GET", "/api/system/update-status", func(c *gin.Context) {
		force := false
		if raw, exists := c.GetQuery("force"); exists {
			var err error
			force, err = strconv.ParseBool(raw)
			if err != nil {
				writeError(c, 400, "invalid_request_error", "force 参数必须是布尔值")
				return
			}
		}
		c.JSON(200, options.Updates.Status(force))
	})
	register("GET", "/api/system/update-task", func(c *gin.Context) { c.JSON(200, options.Updates.Task()) })
	register("POST", "/api/system/update", func(c *gin.Context) {
		if err := options.Updates.Start(c.Request.Context()); err != nil {
			writeError(c, 409, "conflict_error", err.Error())
			return
		}
		writeError(c, 409, "conflict_error", "当前部署不支持在线更新")
	})
}
