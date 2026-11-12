package accounts

import (
	"chatgpt2api/go_backend/ent"
	"chatgpt2api/go_backend/internal/proxy"
	"chatgpt2api/go_backend/internal/upstream"
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type Selector interface {
	Select(context.Context, string) (Account, error)
}
type Lease struct {
	Account  Account
	once     sync.Once
	finish   func(context.Context, bool, int, error) error
	release  func()
	snapshot record
	err      error
}

func (l *Lease) Finish(ctx context.Context, success bool, outputs int, cause error) error {
	l.once.Do(func() {
		if l.finish != nil {
			l.err = l.finish(ctx, success, outputs, cause)
		}
	})
	return l.err
}

// Acquire 让执行服务及测试 Adapter 共用同一预留入口。
func Acquire(ctx context.Context, selector Selector, id string, image bool) (*Lease, error) {
	return AcquireExcluding(ctx, selector, id, image, nil)
}

// AcquireExcluding reserves one slot while excluding accounts already attempted by that image.
func AcquireExcluding(ctx context.Context, selector Selector, id string, image bool, excluded []string) (*Lease, error) {
	if pool, ok := selector.(interface {
		AcquireExcluding(context.Context, string, bool, []string) (*Lease, error)
	}); ok {
		return pool.AcquireExcluding(ctx, id, image, excluded)
	}
	// Legacy adapters cannot select another account reliably. Do not reserve or replay one.
	if len(excluded) > 0 {
		return nil, ErrSelection
	}
	if pool, ok := selector.(interface {
		Acquire(context.Context, string, bool) (*Lease, error)
	}); ok {
		return pool.Acquire(ctx, id, image)
	}
	a, err := selector.Select(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Lease{Account: a}, nil
}
func (r *Repository) Acquire(ctx context.Context, id string, image bool) (*Lease, error) {
	return r.acquire(ctx, id, image, true)
}
func (r *Repository) AcquireExcluding(ctx context.Context, id string, image bool, excluded []string) (*Lease, error) {
	return r.acquireFiltered(ctx, id, image, true, false, func(data map[string]any) bool {
		for _, previous := range excluded {
			if accountID(data) == previous {
				return false
			}
		}
		return true
	})
}
func (r *Repository) acquire(parent context.Context, id string, image, reserve bool) (*Lease, error) {
	return r.acquireFiltered(parent, id, image, reserve, false, nil)
}
func (r *Repository) acquireFiltered(parent context.Context, id string, image, reserve, preflight bool, filter func(map[string]any) bool) (*Lease, error) {
	if id != "" && !validID.MatchString(id) {
		return nil, ErrSelection
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	values, err := r.Runtime(ctx)
	if err != nil {
		return nil, ErrStorage
	}
	limit := number(values["image_account_concurrency"])
	if limit < 1 {
		limit = 1
	}
	limit = min(3, limit)
	items, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := loadGroups(ctx, r.client)
	if err != nil {
		return nil, err
	}
	groupAllowed := func(data map[string]any) bool {
		groupID := text(data["group_id"])
		if groupID == "" {
			return true
		}
		for _, g := range groups {
			if text(g["id"]) == groupID {
				return flag(g["enabled"], true)
			}
		}
		return false
	}
	candidates := []record{}
	for _, rec := range items {
		if id != "" && accountID(rec.data) != id {
			continue
		}
		if !eligible(rec.data, image) || (filter != nil && !filter(rec.data)) {
			continue
		}
		if groupAllowed(rec.data) {
			candidates = append(candidates, rec)
		}
	}
	if len(candidates) == 0 {
		return nil, ErrSelection
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil, context.Canceled
	}
	var chosen record
	found := false
	for n := 0; n < len(candidates); n++ {
		i := (r.next + n) % len(candidates)
		rec := candidates[i]
		if r.maintaining[accountID(rec.data)] == 0 && (!image || r.inflight[accountID(rec.data)] < limit) {
			chosen = rec
			found = true
			r.next = (i + 1) % len(candidates)
			break
		}
	}
	if !found {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	selectedID := accountID(chosen.data)
	// 清理与租约登记共用 r.mu；候选快照可能先于清理，登记前复查持久对象。
	rows, checkErr := r.client.QueryContext(ctx, "SELECT id FROM accounts WHERE id=$1", chosen.row)
	if checkErr != nil {
		r.mu.Unlock()
		return nil, ErrStorage
	}
	exists := rows.Next()
	checkErr = rows.Err()
	rows.Close()
	if checkErr != nil {
		r.mu.Unlock()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrStorage
	}
	if !exists {
		r.mu.Unlock()
		return nil, ErrSelection
	}
	if reserve {
		r.active[selectedID]++
	}
	if image && reserve {
		r.inflight[selectedID]++
	}
	resolver := r.resolve
	r.mu.Unlock()
	var releaseProxy func()
	release := func() {
		if releaseProxy != nil {
			releaseProxy()
		}
		if reserve {
			r.mu.Lock()
			r.active[selectedID]--
			if r.active[selectedID] <= 0 {
				delete(r.active, selectedID)
			}
			if image {
				r.inflight[selectedID]--
				if r.inflight[selectedID] <= 0 {
					delete(r.inflight, selectedID)
				}
			}
			r.mu.Unlock()
		}
	}
	// 仅在生成 POST 之前恢复凭据；刷新失败不会换号或重放生成。
	at, rt, _ := lifecycle(chosen.data)
	if reserve && !preflight && (at == "invalid" || at == "expiring") && rt == "valid" {
		updated, e := r.maintain(parent, chosen, "refresh_access_token")
		if e != nil {
			release()
			return nil, e
		}
		chosen.data = updated
		if !eligible(chosen.data, image) || (filter != nil && !filter(chosen.data)) {
			release()
			return nil, ErrSelection
		}
		checkCtx, stop := context.WithTimeout(parent, r.timeout)
		groups, err = loadGroups(checkCtx, r.client)
		stop()
		if err != nil {
			release()
			return nil, err
		}
		if !groupAllowed(chosen.data) {
			release()
			return nil, ErrSelection
		}
	}
	credential := Credential{AccessToken: text(chosen.data["access_token"]), RefreshToken: text(chosen.data["refresh_token"]), Proxy: text(chosen.data["proxy"])}
	if resolver != nil {
		var group map[string]any
		for _, g := range groups {
			if text(g["id"]) == text(chosen.data["group_id"]) {
				group = g
			}
		}
		if image && reserve {
			credential.Proxy, releaseProxy, err = resolver.AcquireImage(parent, chosen.data, group)
		} else {
			credential.Proxy, err = resolver.Resolve(parent, chosen.data, group)
		}
	} else if credential.Proxy == "direct" {
		credential.Proxy = ""
	} else if strings.HasPrefix(credential.Proxy, "group:") {
		err = ErrSelection
	}
	if err != nil || credential.validate() != nil {
		release()
		if errors.Is(err, proxy.ErrBusy) {
			return nil, ErrBusy
		}
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		if preflight {
			return nil, &preflightCandidateUnavailable{id: selectedID}
		}
		return nil, ErrSelection
	}
	if parent.Err() != nil {
		release()
		return nil, parent.Err()
	}
	lease := &Lease{Account: selectedAccount(chosen.data, credential), release: release, snapshot: chosen}
	if reserve {
		lease.finish = func(ctx context.Context, success bool, outputs int, cause error) error {
			defer release()
			_, err := r.mutate(ctx, "", func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
				for _, rec := range items {
					if accountID(rec.data) != selectedID {
						continue
					}
					key := "fail"
					if success {
						key = "success"
					}
					rec.data[key] = max(0, number(rec.data[key])) + 1
					rec.data["last_used_at"] = time.Now().UTC().Format(time.RFC3339)
					if image && outputs > 0 && !flag(rec.data["image_quota_unknown"], true) {
						rec.data["quota"] = max(0, number(rec.data["quota"])-outputs)
						if number(rec.data["quota"]) == 0 {
							rec.data["image_quota_unknown"] = true
							rec.data["last_quota_estimated_empty_at"] = time.Now().UTC().Format(time.RFC3339)
						}
					}
					var failure *upstream.Failure
					if errors.As(cause, &failure) && failure.Status == 401 && text(rec.data["access_token"]) == lease.Account.AccessToken {
						rec.data["last_remote_check_result"] = "invalid"
						rec.data["last_invalid_at"] = time.Now().UTC().Format(time.RFC3339)
					}
					return true, save(ctx, tx, rec)
				}
				return false, nil
			})
			return err
		}
	} else {
		release()
	}
	return lease, nil
}
