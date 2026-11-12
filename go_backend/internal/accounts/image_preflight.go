package accounts

import (
	"context"
	"errors"
	"strings"
)

var ErrQuotaExhausted = errors.New("所有候选图片账号均被远程确认额度耗尽")

// preflightCandidateUnavailable 区分候选自身出口失败与候选池耗尽，不携带敏感出口信息。
type preflightCandidateUnavailable struct{ id string }

func (e *preflightCandidateUnavailable) Error() string { return ErrSelection.Error() }
func (e *preflightCandidateUnavailable) Unwrap() error { return ErrSelection }

// ImageModelAvailability 只投影本地可调度快照；浏览模型目录不触发远程核验。
func (r *Repository) ImageModelAvailability(parent context.Context) (bool, []string, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	items, err := r.snapshot(ctx)
	if err != nil {
		return false, nil, err
	}
	available := false
	plans := map[string]bool{}
	for _, rec := range items {
		if !eligible(rec.data, true) {
			continue
		}
		available = true
		if normalizeSource(text(rec.data["source_type"])) == "codex" {
			plans[normalizePlan(text(rec.data["type"]))] = true
		}
	}
	result := []string{}
	for _, plan := range []string{"plus", "team", "pro"} {
		if plans[plan] {
			result = append(result, plan)
		}
	}
	return available, result, nil
}

func normalizeSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "cpa", "cpa_json", "remote_cpa", "sub2api":
		return "codex"
	default:
		return "web"
	}
}

func normalizePlan(value string) string {
	key := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch key {
	case "free", "plus", "pro", "prolite", "team", "enterprise":
		return key
	case "business":
		return "team"
	default:
		return ""
	}
}

func selectedAccount(data map[string]any, credential Credential) Account {
	return Account{Credential: credential, ID: accountID(data), Email: text(data["email"]), Source: normalizeSource(text(data["source_type"])), Plan: normalizePlan(text(data["type"]))}
}

func matchesImageAccount(data map[string]any, source string, plans []string) bool {
	if source != "" && normalizeSource(text(data["source_type"])) != normalizeSource(source) {
		return false
	}
	if len(plans) == 0 {
		return true
	}
	plan := normalizePlan(text(data["type"]))
	for _, candidate := range plans {
		if normalized := normalizePlan(candidate); normalized != "" && normalized == plan {
			return true
		}
	}
	return false
}

// AcquireImage 是新图片的唯一核验入口；只读续查沿用固定账号的原租约入口。
func AcquireImage(ctx context.Context, selector Selector, id, source string, plans, excluded []string) (*Lease, error) {
	if pool, ok := selector.(interface {
		AcquireImage(context.Context, string, string, []string, []string) (*Lease, error)
	}); ok {
		return pool.AcquireImage(ctx, id, source, plans, excluded)
	}
	// 隔离执行 Adapter 不持有真实账号仓储，但仍不能绕过显式来源/套餐条件。
	lease, err := AcquireExcluding(ctx, selector, id, true, excluded)
	if err == nil && !matchesImageAccount(map[string]any{"source_type": lease.Account.Source, "type": lease.Account.Plan}, source, plans) {
		_ = lease.Finish(ctx, false, 0, ErrSelection)
		return nil, ErrSelection
	}
	return lease, err
}

func (r *Repository) AcquireImage(parent context.Context, id, source string, plans, excluded []string) (*Lease, error) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	defer cancel()
	if r.ctx.Err() != nil {
		return nil, context.Canceled
	}
	attempted := make(map[string]bool, len(excluded)+20)
	for _, value := range excluded {
		attempted[value] = true
	}
	quota, unavailable := false, false
	for attempt := 0; attempt < 20; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lease, err := r.acquireFiltered(ctx, id, true, true, true, func(data map[string]any) bool {
			return !attempted[accountID(data)] && matchesImageAccount(data, source, plans)
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var candidate *preflightCandidateUnavailable
			if errors.As(err, &candidate) {
				attempted[candidate.id] = true
				unavailable = true
				continue
			}
			if attempt == 0 || !errors.Is(err, ErrSelection) {
				return nil, err
			}
			break
		}
		attempted[lease.Account.ID] = true
		// 预检落选不属于一次生成，不增加账号成功/失败或最近使用计数。
		discard := func() { lease.once.Do(lease.release) }
		updated, err := r.maintainAt(ctx, lease.snapshot, "image_preflight", &lease.Account.Proxy)
		if ctx.Err() != nil {
			discard()
			return nil, ctx.Err()
		}
		if err != nil {
			discard()
			if errors.Is(err, ErrStorage) {
				return nil, err
			}
			unavailable = true
			continue
		}
		checkCtx, done := context.WithTimeout(ctx, r.timeout)
		groups, err := loadGroups(checkCtx, r.client)
		done()
		if err != nil {
			discard()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrStorage
		}
		groupAllowed := text(updated["group_id"]) == ""
		for _, group := range groups {
			if text(group["id"]) == text(updated["group_id"]) {
				groupAllowed = flag(group["enabled"], true)
			}
		}
		if eligible(updated, true) && groupAllowed && matchesImageAccount(updated, source, plans) {
			lease.Account = selectedAccount(updated, Credential{AccessToken: text(updated["access_token"]), RefreshToken: text(updated["refresh_token"]), Proxy: lease.Account.Proxy})
			return lease, nil
		}
		discard()
		if text(updated["status"]) == "限流" {
			quota = true
		} else {
			unavailable = true
		}
	}
	if quota && !unavailable {
		return nil, ErrQuotaExhausted
	}
	return nil, ErrSelection
}
