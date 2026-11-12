package settings

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// NewConsoleHandler 按当前业务设置筛选进程输出；调用记录与领域日志由各自所有者保存。
func NewConsoleHandler(next slog.Handler, runtime func(context.Context) (map[string]any, error)) slog.Handler {
	return &consoleHandler{next: next, runtime: runtime}
}

type consoleHandler struct {
	next    slog.Handler
	runtime func(context.Context) (map[string]any, error)
}

func (h *consoleHandler) Enabled(parent context.Context, level slog.Level) bool {
	if !h.next.Enabled(parent, level) {
		return false
	}
	levels := map[string]bool{}
	if h.runtime != nil {
		// 日志读取有独立短预算；已取消请求仍能输出默认错误日志。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 100*time.Millisecond)
		defer cancel()
		if values, err := h.runtime(ctx); err == nil {
			if selected, ok := values["log_levels"].([]any); ok {
				for _, item := range selected {
					if text, ok := item.(string); ok {
						name := strings.ToLower(strings.TrimSpace(text))
						if name == "debug" || name == "info" || name == "warning" || name == "error" {
							levels[name] = true
						}
					}
				}
			}
		}
	}
	if len(levels) == 0 {
		return level >= slog.LevelInfo
	}
	name := "debug"
	switch {
	case level >= slog.LevelError:
		name = "error"
	case level >= slog.LevelWarn:
		name = "warning"
	case level >= slog.LevelInfo:
		name = "info"
	}
	return levels[name]
}
func (h *consoleHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, record)
}
func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &consoleHandler{next: h.next.WithAttrs(attrs), runtime: h.runtime}
}
func (h *consoleHandler) WithGroup(name string) slog.Handler {
	return &consoleHandler{next: h.next.WithGroup(name), runtime: h.runtime}
}
