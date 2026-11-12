package accounts

import (
	"chatgpt2api/go_backend/ent"
	"context"
	"database/sql"
)

type CleanupResult struct {
	DryRun            bool `json:"dry_run"`
	Invalid           int  `json:"invalid"`
	RateLimited       int  `json:"rate_limited"`
	TotalRemoved      int  `json:"total_removed"`
	RemoveInvalid     bool `json:"auto_remove_invalid_accounts"`
	RemoveRateLimited bool `json:"auto_remove_rate_limited_accounts"`
}

// Cleanup 仅按上游确认的持久状态清理，AT 临时失效但 RT 可恢复不等同于“异常”。
func (r *Repository) Cleanup(parent context.Context, removeInvalid, removeRateLimited, dryRun bool) (CleanupResult, error) {
	result := CleanupResult{DryRun: dryRun, RemoveInvalid: removeInvalid, RemoveRateLimited: removeRateLimited}
	if dryRun {
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		defer cancel()
		tx, err := r.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return result, ErrStorage
		}
		defer tx.Rollback()
		items, err := load(ctx, tx)
		if err != nil {
			return result, err
		}
		// 先释放数据库连接，再检查运行时租约，保持与 acquire 的锁顺序一致。
		if err = tx.Commit(); err != nil {
			return result, ErrStorage
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		count := 0
		for _, rec := range cleanupCandidates(items, removeInvalid, removeRateLimited) {
			if count >= MaxBatch {
				break
			}
			if r.active[accountID(rec.data)] > 0 || r.maintaining[accountID(rec.data)] > 0 {
				continue
			}
			status := text(rec.data["status"])
			if removeInvalid && status == "异常" {
				result.Invalid++
				count++
			} else if removeRateLimited && status == "限流" {
				result.RateLimited++
				count++
			}
		}
		result.TotalRemoved = count
		return result, nil
	}
	release, err := r.processingSlot(parent)
	if err != nil {
		return result, err
	}
	defer release()
	// acquire/maintain 的登记与删除事务在同一进程锁内互斥；事务内读取最新状态，避免删除过期预览的对象。
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err = r.mutate(parent, "", func(ctx context.Context, tx *ent.Tx, items []record, operation *Operation) (bool, error) {
		for _, rec := range cleanupCandidates(items, removeInvalid, removeRateLimited) {
			if result.TotalRemoved >= MaxBatch {
				break
			}
			id := accountID(rec.data)
			if r.active[id] > 0 || r.maintaining[id] > 0 {
				continue
			}
			status := text(rec.data["status"])
			kind := ""
			if removeInvalid && status == "异常" {
				kind = "invalid"
			} else if removeRateLimited && status == "限流" {
				kind = "limited"
			}
			if kind == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", rec.row); err != nil {
				return false, ErrStorage
			}
			if kind == "invalid" {
				result.Invalid++
			} else {
				result.RateLimited++
			}
			result.TotalRemoved++
			operation.Removed++
			operation.RemovedIDs = append(operation.RemovedIDs, id)
		}
		return result.TotalRemoved > 0, nil
	})
	if err != nil {
		return CleanupResult{DryRun: false, RemoveInvalid: removeInvalid, RemoveRateLimited: removeRateLimited}, err
	}
	return result, nil
}

// 按持久状态分两组保留既有顺序，在批次上限内优先清理异常账号。
func cleanupCandidates(items []record, removeInvalid, removeLimited bool) []record {
	out := make([]record, 0, len(items))
	for _, status := range []string{"异常", "限流"} {
		if status == "异常" && !removeInvalid || status == "限流" && !removeLimited {
			continue
		}
		for _, rec := range items {
			if text(rec.data["status"]) == status {
				out = append(out, rec)
			}
		}
	}
	return out
}

// reserveMaintenance 与清理共用进程锁，在慢速维护前重读一条当前记录，不恢复已删除账号。
func (r *Repository) reserveMaintenance(ctx context.Context, rec record, background bool) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, context.Canceled
	}
	id := accountID(rec.data)
	if r.maintaining[id] > 0 || (background && r.active[id] > 0) {
		return nil, ErrBusy
	}
	rows, err := r.client.QueryContext(ctx, "SELECT id FROM accounts WHERE id=$1", rec.row)
	if err != nil {
		return nil, ErrStorage
	}
	found := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrStorage
	}
	if !found {
		return nil, ErrNotFound
	}
	r.maintaining[id]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.maintaining[id]--
		if r.maintaining[id] <= 0 {
			delete(r.maintaining, id)
		}
	}, nil
}
