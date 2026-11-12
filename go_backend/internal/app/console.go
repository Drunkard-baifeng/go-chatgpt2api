package app

import (
	"bytes"
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 由 scripts/build.mjs 或容器构建生成；缺少资源时编译失败，不生成空控制台。
//
//go:embed all:console/dist
var consoleFiles embed.FS

func consoleHandler() (gin.HandlerFunc, error) {
	const root = "console/dist/"
	index, err := consoleFiles.ReadFile(root + "index.html")
	if err != nil || len(index) == 0 {
		return nil, errors.New("控制台资源不完整，请从项目根目录运行 node scripts/build.mjs")
	}
	return func(c *gin.Context) {
		r := c.Request
		name := strings.TrimPrefix(r.URL.Path, "/")
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			(name != "" && !fs.ValidPath(name)) || strings.ContainsAny(name, "\\\x00") {
			writeError(c, http.StatusNotFound, "not_found_error", "接口不存在")
			return
		}
		// API 和业务资产归原处理器所有，未知接口也不能回退到 HTML。
		first, _, _ := strings.Cut(name, "/")
		switch first {
		case "api", "v1", "auth", "health", "version", "images", "image-thumbnails", "files":
			writeError(c, http.StatusNotFound, "not_found_error", "接口不存在")
			return
		}
		if name != "" && name != "index.html" {
			file, err := consoleFiles.Open(root + name)
			if err == nil {
				defer file.Close()
				info, statErr := file.Stat()
				if statErr == nil && info.Mode().IsRegular() {
					c.Header("X-Content-Type-Options", "nosniff")
					c.Header("Cache-Control", "no-cache")
					if first == "assets" {
						c.Header("Cache-Control", "public, max-age=31536000, immutable")
					}
					// embed 中的普通文件实现 ReadSeeker，直接服务内存资源，无磁盘依赖。
					http.ServeContent(c.Writer, r, name, time.Time{}, file.(io.ReadSeeker))
					return
				}
			}
			if first == "assets" || first == "vendor" || path.Ext(name) != "" || strings.HasPrefix(name, ".") {
				writeError(c, http.StatusNotFound, "not_found_error", "资源不存在")
				return
			}
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Content-Type-Options", "nosniff")
		http.ServeContent(c.Writer, r, "index.html", time.Time{}, bytes.NewReader(index))
	}, nil
}
