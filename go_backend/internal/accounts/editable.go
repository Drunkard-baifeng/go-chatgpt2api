package accounts

import (
	"context"
	"strings"
)

// AcquireEditable 在规范账号选择、刷新、代理及租约路径中增加付费计划约束。
// 文本限流账号仍沿用文本请求的可用性；不消耗图片配额。
func (r *Repository) AcquireEditable(ctx context.Context) (*Lease, error) {
	return r.acquireFiltered(ctx, "", false, true, false, editablePlan)
}

func editablePlan(data map[string]any) bool {
	switch strings.ToLower(strings.TrimSpace(text(data["type"]))) {
	case "plus", "team", "business", "pro", "enterprise":
		return true
	}
	return false
}
