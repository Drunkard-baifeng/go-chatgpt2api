package accounts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// StartLifecycle 与 Repository 共用取消和关闭生命周期。启动即扫描一轮，之后按当前设置等待。
func (r *Repository) StartLifecycle(parent context.Context, logger *slog.Logger) error {
	if err := parent.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return context.Canceled
	}
	if r.lifecycleStarted {
		r.mu.Unlock()
		return ErrBusy
	}
	if r.remote == nil {
		r.mu.Unlock()
		return ErrRemote
	}
	r.lifecycleStarted = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithCancel(r.ctx)
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		defer cancel()
		for ctx.Err() == nil {
			if err := r.runLifecycle(ctx, logger); err != nil && ctx.Err() == nil && logger != nil {
				logger.Warn("后台账号维护扫描失败")
			}
			values, err := r.Runtime(ctx)
			if err != nil && ctx.Err() == nil && logger != nil {
				logger.Warn("后台账号维护间隔读取失败，使用默认间隔")
			}
			timer := time.NewTimer(lifecycleInterval(values))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return nil
}

func lifecycleInterval(values map[string]any) time.Duration {
	value, exists := values["refresh_account_interval_minute"]
	if !exists {
		return 5 * time.Minute
	}
	minutes := max(1, number(value))
	maximum := int((time.Duration(1<<63 - 1)) / time.Minute)
	return time.Duration(min(minutes, maximum)) * time.Minute
}

func lifecycleEligible(data map[string]any, phase string, now time.Time) bool {
	if text(data["access_token"]) == "" {
		return false
	}
	pending := text(data["last_remote_check_result"]) == "pending"
	switch phase {
	case "verify_pending_auth":
		return pending && text(data["pending_auth_scope"]) == "image"
	case "sync_limited_account":
		return !pending && text(data["status"]) == "限流"
	case "renew_expiring_access_token":
		status := text(data["status"])
		if pending || (status != "正常" && status != "限流") || text(data["refresh_token"]) == "" || text(data["refresh_token_invalid_at"]) != "" || timestamp(data["refresh_token_invalid_at"]) != nil {
			return false
		}
		expires := number(claims(text(data["access_token"]))["exp"])
		if expires <= 0 || int64(expires) > now.Add(24*time.Hour).Unix() {
			return false
		}
		failed := timestamp(data["last_token_refresh_error_at"])
		if failed != nil {
			at := int64(number(failed))
			if unix, ok := failed.(int64); ok {
				at = unix
			}
			if now.Sub(time.Unix(at, 0)) < 5*time.Minute {
				return false
			}
		}
		return true
	}
	return false
}

func (r *Repository) runLifecycle(ctx context.Context, logger *slog.Logger) error {
	for _, phase := range []string{"verify_pending_auth", "renew_expiring_access_token", "sync_limited_account"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := r.Runtime(ctx)
		if err != nil {
			return err
		}
		items, err := r.snapshot(ctx)
		if err != nil {
			return err
		}
		selected := make([]record, 0, len(items))
		now := time.Now()
		for _, rec := range items {
			if lifecycleEligible(rec.data, phase, now) {
				selected = append(selected, rec)
			}
		}
		if len(selected) == 0 {
			continue
		}
		workers := number(values["account_processing_concurrency"])
		if workers < 1 {
			workers = 30
		}
		workers = min(len(selected), min(100, workers))
		queue := make(chan record)
		var group sync.WaitGroup
		var lock sync.Mutex
		failed := 0
		for range workers {
			group.Add(1)
			go func() {
				defer group.Done()
				for rec := range queue {
					_, err := r.maintain(ctx, rec, "background_"+phase)
					if err != nil && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
						lock.Lock()
						failed++
						lock.Unlock()
					}
				}
			}()
		}
	feed:
		for _, rec := range selected {
			select {
			case queue <- rec:
			case <-ctx.Done():
				break feed
			}
		}
		close(queue)
		group.Wait()
		if failed > 0 && logger != nil {
			logger.Warn("部分后台账号维护失败", "phase", phase, "failed", failed)
		}
	}
	return ctx.Err()
}
