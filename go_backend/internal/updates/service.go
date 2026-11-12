// Package updates 投影当前 Go 部署的升级能力，不安装不匹配的 Python 发布包。
package updates

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrUnsupported = errors.New("当前 Go 部署没有匹配的在线更新包，请使用部署工具拉取新镜像或更新源码并重新部署。")

type Service struct {
	version string
	started time.Time
}

func New(version string) *Service {
	return &Service{version: strings.TrimSpace(version), started: time.Now()}
}
func (s *Service) Status(_ bool) map[string]any {
	return map[string]any{"current_tag": s.version, "latest_tag": "", "update_available": false, "release_url": "", "status_label": "通过部署工具升级", "status_message": ErrUnsupported.Error(), "tone": "warning", "changelog": "", "can_update": false}
}
func (s *Service) Task() map[string]any {
	return map[string]any{"task_id": "", "state": "idle", "stage": "idle", "current": 0, "total": 0, "status_label": "未启动更新", "message": ErrUnsupported.Error(), "tone": "info", "busy": false, "current_tag": s.version, "latest_tag": "", "error": "", "updated_at": s.started.UTC().Format(time.RFC3339Nano), "events": []any{}}
}
func (s *Service) Start(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrUnsupported
}
