package accounts

import (
	"chatgpt2api/go_backend/ent"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Remote 只承载慢速上游步骤；仓储拥有凭据 CAS、状态与容量。
type Remote interface {
	Sync(context.Context, Credential) (map[string]any, error)
	Refresh(context.Context, Credential) (Credential, error)
}

var ErrRemote = errors.New("上游账号维护失败")
var ErrRefreshTerminal = errors.New("RT 已被上游确认失效，请重新授权")
var ErrRemoteInvalid = errors.New("AT 已被上游确认失效")

type Job struct {
	Total, Processed int
	Done             bool
	Stage, Error     string
	Result           Operation
	Created, Updated time.Time
}

func (r *Repository) Configure(runtime func(context.Context) (map[string]any, error), resolve ProxyResolver, remote Remote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtime = runtime
	r.resolve = resolve
	r.remote = remote
}
func (r *Repository) Runtime(ctx context.Context) (map[string]any, error) {
	r.mu.Lock()
	runtime := r.runtime
	r.mu.Unlock()
	if runtime == nil {
		return map[string]any{"account_processing_concurrency": 30, "image_account_concurrency": 1}, nil
	}
	return runtime(ctx)
}
func (r *Repository) processingSlot(ctx context.Context) (func(), error) {
	settings, err := r.Runtime(ctx)
	if err != nil {
		return nil, ErrStorage
	}
	limit := number(settings["account_processing_concurrency"])
	if limit == 0 {
		limit = 30
	}
	limit = max(1, min(100, limit))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, context.Canceled
	}
	if r.processing >= limit {
		return nil, ErrBusy
	}
	r.processing++
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); r.processing--; r.mu.Unlock() }) }, nil
}
func (r *Repository) waitProcessing(ctx context.Context) (func(), error) {
	for {
		release, err := r.processingSlot(ctx)
		if !errors.Is(err, ErrBusy) {
			return release, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r *Repository) Close() { r.mu.Lock(); r.closing = true; r.cancel(); r.mu.Unlock(); r.wg.Wait() }
func newJobID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(raw[:]), nil
}
func (r *Repository) pruneLocked() {
	now := time.Now()
	for id, job := range r.jobs {
		if job.Done && now.Sub(job.Updated) > 10*time.Minute {
			delete(r.jobs, id)
		}
	}
	for len(r.jobs) >= 64 {
		oldest := ""
		var at time.Time
		for id, job := range r.jobs {
			if job.Done && (oldest == "" || job.Updated.Before(at)) {
				oldest = id
				at = job.Updated
			}
		}
		if oldest == "" {
			break
		}
		delete(r.jobs, oldest)
	}
}
func (r *Repository) Completed(result Operation) (string, error) {
	id, err := newJobID()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	r.jobs[id] = &Job{Total: len(result.UpdatedIDs) + len(result.RemovedIDs) + len(result.Errors), Processed: len(result.Events), Done: true, Stage: "completed", Result: copyOperation(result), Created: time.Now(), Updated: time.Now()}
	return id, nil
}
func (r *Repository) Progress(id string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	job := r.jobs[id]
	if job == nil {
		return nil, ErrNotFound
	}
	result := copyOperation(job.Result)
	present(&result)
	stageLabel := "同步账号"
	if job.Stage == "refresh_access_token" {
		stageLabel = "续期 AT"
	}
	if job.Done {
		stageLabel = "处理完成"
	}
	return map[string]any{"total": job.Total, "processed": job.Processed, "done": job.Done, "error": optional(job.Error), "stage": job.Stage, "stage_label": stageLabel, "status_label": result.StatusLabel, "tone": result.Tone, "message": result.Message, "summary_items": result.SummaryItems, "events": append([]map[string]any{}, result.Events...), "result": result, "status_counts": map[string]int{}, "total_quota": 0}, nil
}
func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func (r *Repository) Start(parent context.Context, cmd Command, action string) (map[string]any, error) {
	items, err := r.snapshot(parent)
	if err != nil {
		return nil, err
	}
	selected, missing, err := targets(items, cmd, false)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, invalid("请选择有效账号")
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	settings, err := r.Runtime(parent)
	if err != nil {
		return nil, ErrStorage
	}
	workers := number(settings["account_processing_concurrency"])
	if workers < 1 {
		workers = 30
	}
	workers = min(len(selected), min(100, workers))
	r.mu.Lock()
	r.pruneLocked()
	active := 0
	for _, job := range r.jobs {
		if !job.Done {
			active++
		}
	}
	if r.closing || active >= 4 {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	initial := emptyOperation()
	ids := []string{}
	for _, rec := range selected {
		ids = append(ids, accountID(rec.data))
	}
	for _, id := range missing {
		operationError(&initial, id, "account_not_found", "账号不存在", action)
	}
	r.jobs[id] = &Job{Total: len(selected) + len(missing), Processed: len(missing), Stage: action, Result: initial, Created: time.Now(), Updated: time.Now()}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(r.ctx, 2*time.Hour)
		defer cancel()
		queue := make(chan record)
		var group sync.WaitGroup
		for i := 0; i < workers; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				for rec := range queue {
					patch, err := r.maintain(ctx, rec, action)
					r.mu.Lock()
					job := r.jobs[id]
					if err != nil {
						code := "upstream_maintenance_failed"
						message := "账号上游维护失败"
						switch {
						case errors.Is(err, ErrConflict):
							code, message = "credentials_changed", "凭据或状态已变更，本次结果未覆盖新数据"
						case errors.Is(err, ErrRefreshTerminal):
							code, message = "refresh_token_invalid", ErrRefreshTerminal.Error()
						case errors.Is(err, context.Canceled):
							code, message = "cancelled", "操作已取消"
						case errors.Is(err, context.DeadlineExceeded):
							code, message = "timeout", "账号维护超时"
						case errors.Is(err, ErrNotFound):
							code, message = "account_not_found", ErrNotFound.Error()
						}
						operationError(&job.Result, accountID(rec.data), code, message, action)
					} else {
						job.Result.UpdatedIDs = append(job.Result.UpdatedIDs, accountID(rec.data))
						job.Result.Updated++
						if action == "refresh_access_token" {
							job.Result.Refreshed++
						} else {
							job.Result.Synced++
						}
						job.Result.Items = append(job.Result.Items, project(patch, nil, 0))
						event(&job.Result, accountID(rec.data), action, "success", "账号维护成功")
					}
					job.Processed++
					job.Updated = time.Now()
					r.mu.Unlock()
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
		r.mu.Lock()
		job := r.jobs[id]
		if ctx.Err() != nil {
			job.Error = "账号操作已取消或超时"
			processed := map[string]bool{}
			for _, id := range job.Result.UpdatedIDs {
				processed[id] = true
			}
			for _, raw := range job.Result.Errors {
				if m, ok := raw.(map[string]any); ok {
					processed[text(m["id"])] = true
				}
			}
			for _, rec := range selected {
				if id := accountID(rec.data); !processed[id] {
					operationError(&job.Result, id, "cancelled", "操作在处理前已取消", action)
					job.Processed++
				}
			}
		}
		job.Done = true
		job.Stage = "completed"
		job.Updated = time.Now()
		present(&job.Result)
		r.mu.Unlock()
	}()
	return map[string]any{"progress_id": id, "target_ids": ids, "errors": initial.Errors}, nil
}
func copyOperation(value Operation) Operation {
	data, _ := json.Marshal(value)
	var out Operation
	_ = json.Unmarshal(data, &out)
	return out
}
func (r *Repository) SyncImported(ctx context.Context, result *Operation) error {
	ids := append([]string{}, result.UpdatedIDs...)
	values, err := r.Runtime(ctx)
	if err != nil {
		return err
	}
	workers := number(values["account_processing_concurrency"])
	if workers < 1 {
		workers = 30
	}
	workers = min(len(ids), min(100, workers))
	queue := make(chan string)
	var wg sync.WaitGroup
	var lock sync.Mutex
	fresh := map[string]any{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range queue {
				rec, e := r.raw(ctx, id)
				var updated map[string]any
				if e == nil {
					updated, e = r.maintain(ctx, rec, "sync_account")
				}
				lock.Lock()
				if e != nil {
					operationError(result, id, "sync_failed", "账号已保存，但同步失败", "sync_account")
				} else {
					result.Synced++
					fresh[id] = project(updated, nil, 0)
					event(result, id, "sync_account", "success", "账号同步成功")
				}
				lock.Unlock()
			}
		}()
	}
	for _, id := range ids {
		queue <- id
	}
	close(queue)
	wg.Wait()
	for i, item := range result.Items {
		if m, ok := item.(map[string]any); ok {
			if value := fresh[text(m["id"])]; value != nil {
				result.Items[i] = value
			}
		}
	}
	present(result)
	return ctx.Err()
}
func (r *Repository) maintain(parent context.Context, snapshot record, action string) (map[string]any, error) {
	return r.maintainAt(parent, snapshot, action, nil)
}

// 已预留的图片出口贯穿核验与生成，避免代理组再次选择另一节点。
func (r *Repository) maintainAt(parent context.Context, snapshot record, action string, resolvedProxy *string) (map[string]any, error) {
	release, err := r.waitProcessing(parent)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	background := strings.HasPrefix(action, "background_")
	releaseAccount, err := r.reserveMaintenance(ctx, snapshot, background)
	if err != nil {
		return nil, err
	}
	defer releaseAccount()
	if background {
		phase := strings.TrimPrefix(action, "background_")
		snapshot, err = r.raw(ctx, accountID(snapshot.data))
		if err != nil {
			return nil, err
		}
		// 等待全局容量期间账号可能已经续期、停用或变更；只操作最新仍符合本阶段条件的对象。
		if !lifecycleEligible(snapshot.data, phase, time.Now()) {
			return snapshot.data, nil
		}
		action = "sync_account"
		if phase == "renew_expiring_access_token" {
			action = "refresh_access_token"
		}
	}
	r.mu.Lock()
	remote, resolve := r.remote, r.resolve
	r.mu.Unlock()
	if remote == nil {
		return nil, ErrRemote
	}
	old := snapshot.data
	credential := Credential{AccessToken: text(old["access_token"]), RefreshToken: text(old["refresh_token"]), Proxy: text(old["proxy"])}
	if resolvedProxy != nil {
		credential.Proxy = *resolvedProxy
	} else if resolve != nil {
		groups, err := loadGroups(ctx, r.client)
		if err != nil {
			return nil, err
		}
		var group map[string]any
		for _, g := range groups {
			if text(g["id"]) == text(old["group_id"]) {
				group = g
			}
		}
		credential.Proxy, err = resolve.Resolve(ctx, old, group)
		if err != nil {
			return nil, ErrRemote
		}
	}
	patch := map[string]any{}
	// 已收到的轮换结果和维护诊断各自使用独立短预算收口，不延长上游网络调用。
	commit := func(patch map[string]any, credentialsOnly bool) (map[string]any, error) {
		commitCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer stop()
		var updated map[string]any
		_, err := r.mutate(commitCtx, "", func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
			for _, rec := range items {
				if accountID(rec.data) != accountID(old) {
					continue
				}
				if text(rec.data["access_token"]) != text(old["access_token"]) || text(rec.data["refresh_token"]) != text(old["refresh_token"]) {
					return false, ErrConflict
				}
				if !credentialsOnly && (text(rec.data["status"]) != text(old["status"]) || text(rec.data["proxy"]) != text(old["proxy"]) || text(rec.data["group_id"]) != text(old["group_id"])) {
					return false, ErrConflict
				}
				for _, other := range items {
					if token := text(patch["access_token"]); token != "" && other.row != rec.row && text(other.data["access_token"]) == token {
						return false, ErrConflict
					}
				}
				for key, value := range patch {
					rec.data[key] = value
				}
				rec.data = normalize(rec.data)
				if err := save(ctx, tx, rec); err != nil {
					return false, err
				}
				updated = rec.data
				return true, nil
			}
			return false, ErrNotFound
		})
		return updated, err
	}
	var operationErr error
	refreshed := false
	refresh := func() error {
		if credential.RefreshToken == "" {
			return invalid("账号没有 RT，无法续期")
		}
		updated, e := remote.Refresh(ctx, credential)
		if e != nil {
			return e
		}
		if updated.RefreshToken == "" {
			updated.RefreshToken = credential.RefreshToken
		}
		if !validateToken(updated.AccessToken) || !validateToken(updated.RefreshToken) {
			return ErrRemote
		}
		rotated := map[string]any{"access_token": updated.AccessToken, "refresh_token": updated.RefreshToken, "last_token_refresh_at": time.Now().UTC().Format(time.RFC3339), "last_token_refresh_error": nil, "last_token_refresh_error_at": nil, "refresh_token_invalid_at": nil, "last_remote_check_result": nil}
		saved, e := commit(rotated, true)
		if e != nil {
			return e
		}
		old = saved
		credential.AccessToken = updated.AccessToken
		credential.RefreshToken = updated.RefreshToken
		refreshed = true
		return nil
	}
	if action == "refresh_access_token" {
		operationErr = refresh()
		if operationErr == nil {
			return old, ctx.Err()
		}
	} else {
		at, rt, _ := lifecycle(old)
		// 后台额度/待核验阶段先查询真实登录态；临期续期由独立阶段负责，401仍可触发RT恢复。
		if !background && (at == "invalid" || (action == "image_preflight" && at == "expiring")) && rt == "valid" {
			operationErr = refresh()
		}
		if operationErr == nil {
			data, e := remote.Sync(ctx, credential)
			if errors.Is(e, ErrRemoteInvalid) && !refreshed && rt == "valid" {
				e = refresh()
				if e == nil {
					data, e = remote.Sync(ctx, credential)
				}
			}
			operationErr = e
			if e == nil {
				for _, key := range []string{"email", "user_id", "type", "quota", "image_quota_unknown", "limits_progress", "default_model_slug", "restore_at", "status"} {
					if value, ok := data[key]; ok {
						patch[key] = value
					}
				}
			}
		}
		patch["last_remote_check_attempt_at"] = time.Now().UTC().Format(time.RFC3339)
		if operationErr == nil {
			patch["last_remote_check_result"] = "ok"
			patch["last_remote_checked_at"] = time.Now().UTC().Format(time.RFC3339)
			patch["last_remote_check_error"] = nil
		}
	}
	if operationErr != nil {
		at := time.Now().UTC().Format(time.RFC3339)
		if action == "refresh_access_token" || errors.Is(operationErr, ErrRefreshTerminal) {
			patch["last_token_refresh_error"] = "AT 续期失败"
			patch["last_token_refresh_error_at"] = at
		}
		if errors.Is(operationErr, ErrRefreshTerminal) {
			patch["refresh_token_invalid_at"] = at
		}
		if action != "refresh_access_token" {
			patch["last_remote_check_result"] = "error"
			patch["last_remote_check_error"] = "账号同步失败"
			if errors.Is(operationErr, ErrRemoteInvalid) {
				patch["last_remote_check_result"] = "invalid"
				patch["last_invalid_at"] = at
			}
		}
	}
	// 手动停用不受远程额度/计划信息覆盖。
	if text(old["status"]) == "禁用" {
		patch["status"] = "禁用"
	}
	updated, err := commit(patch, false)
	if err != nil {
		return nil, err
	}
	if operationErr != nil {
		return nil, operationErr
	}
	return updated, nil
}
