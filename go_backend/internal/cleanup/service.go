// Package cleanup 拥有保留期清理的单进程调度、状态与取消；删除仍由各领域所有者执行。
package cleanup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/observability"
	"chatgpt2api/go_backend/internal/settings"
)

var (
	ErrInput   = errors.New("清理参数无效，保留时长必须为 1 至 876000 小时")
	ErrBusy    = errors.New("已有清理操作正在执行，请稍后重试")
	ErrStorage = errors.New("清理服务暂不可用，请稍后重试")
)

const interval = 30 * time.Minute

type RetentionRequest struct {
	LogHours   *int `json:"log_retention_hours"`
	ImageHours *int `json:"image_retention_hours"`
}
type AccountRequest struct {
	RemoveInvalid     *bool `json:"auto_remove_invalid_accounts"`
	RemoveRateLimited *bool `json:"auto_remove_rate_limited_accounts"`
}
type RetentionResult struct {
	DryRun         bool                             `json:"dry_run"`
	Logs           observability.LogRetentionResult `json:"logs"`
	Images         images.RetentionResult           `json:"images"`
	TotalRemoved   int64                            `json:"total_removed"`
	TotalSizeBytes int64                            `json:"total_size_bytes"`
}

type Service struct {
	db              *sql.DB
	settings        *settings.Service
	accounts        *accounts.Repository
	assets          *images.ImageStorageService
	logs            *observability.Service
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	started, closed bool
	busy            chan struct{}
	done            chan struct{}
	wg              sync.WaitGroup
}

func New(db *sql.DB, settings *settings.Service, accounts *accounts.Repository, assets *images.ImageStorageService, logs *observability.Service) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{db: db, settings: settings, accounts: accounts, assets: assets, logs: logs, ctx: ctx, cancel: cancel, busy: make(chan struct{}, 1), done: make(chan struct{})}
}

// Start 不在启动时删除数据，首次定时执行在 30 分钟后；重复调用不创建第二个调度器。
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return context.Canceled
	}
	if s.started {
		return nil
	}
	if err := s.updateState(ctx, map[string]any{"running": false, "next_run_at": timestamp(time.Now().Add(interval))}); err != nil {
		return err
	}
	s.started = true
	s.wg.Add(1)
	go s.schedule()
	return nil
}
func (s *Service) schedule() {
	defer s.wg.Done()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			_, _ = s.Retention(s.ctx, RetentionRequest{}, false)
			_, _ = s.Accounts(s.ctx, AccountRequest{}, false)
			if s.ctx.Err() != nil {
				return
			}
			next := time.Now().Add(interval)
			_ = s.updateState(s.ctx, map[string]any{"next_run_at": timestamp(next)})
			timer.Reset(time.Until(next))
		}
	}
}

func (s *Service) begin(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, context.Canceled
	}
	if parent.Err() != nil {
		return nil, nil, parent.Err()
	}
	select {
	case s.busy <- struct{}{}:
	default:
		return nil, nil, ErrBusy
	}
	s.wg.Add(1)
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel(); <-s.busy; s.wg.Done() }, nil
}
func validHours(value *int) bool { return value == nil || (*value >= 1 && *value <= 876000) }
func (s *Service) runtime(ctx context.Context) (map[string]any, error) {
	if s.settings == nil {
		return nil, ErrStorage
	}
	values, err := s.settings.Runtime(ctx)
	if err != nil {
		return nil, safeError(ctx, err)
	}
	return values, nil
}
func hours(values map[string]any, key string, provided *int) (int, error) {
	if provided != nil {
		return *provided, nil
	}
	v, ok := values[key].(float64)
	if !ok || v < 1 || v > 876000 {
		return 0, ErrInput
	}
	return int(v), nil
}

func (s *Service) Retention(parent context.Context, request RetentionRequest, dryRun bool) (result RetentionResult, err error) {
	result.DryRun = dryRun
	if !validHours(request.LogHours) || !validHours(request.ImageHours) {
		return result, ErrInput
	}
	ctx, done, err := s.begin(parent)
	if err != nil {
		return result, err
	}
	defer done()
	values, err := s.runtime(ctx)
	if err != nil {
		return result, err
	}
	logHours, err := hours(values, "log_retention_hours", request.LogHours)
	if err != nil {
		return result, err
	}
	imageHours, err := hours(values, "image_retention_hours", request.ImageHours)
	if err != nil {
		return result, err
	}
	if s.logs == nil || s.assets == nil {
		return result, ErrStorage
	}
	failures := map[string]error{"logs": nil, "images": nil}
	if !dryRun {
		if err = s.markStarted(ctx); err != nil {
			return result, err
		}
		defer func() {
			if e := s.markFinished(map[string]any{"logs": result.Logs.Removed, "images": result.Images.Removed}, failures); err == nil {
				err = e
			}
		}()
	}
	result.Logs, err = s.logs.CleanupLogs(ctx, logHours, dryRun)
	if err != nil {
		failures["logs"] = safeError(ctx, err)
		return result, safeError(ctx, err)
	}
	result.Images, err = s.assets.CleanupRetention(ctx, imageHours, dryRun)
	result.TotalRemoved = result.Logs.Removed + int64(result.Images.Removed)
	result.TotalSizeBytes = result.Logs.RemovedSizeBytes + result.Images.RemovedSizeBytes
	if err != nil {
		failures["images"] = safeError(ctx, err)
		return result, safeError(ctx, err)
	}
	return result, nil
}

func (s *Service) Accounts(parent context.Context, request AccountRequest, dryRun bool) (result accounts.CleanupResult, err error) {
	ctx, done, err := s.begin(parent)
	if err != nil {
		return result, err
	}
	defer done()
	values, err := s.runtime(ctx)
	if err != nil {
		return result, err
	}
	removeInvalid, _ := values["auto_remove_invalid_accounts"].(bool)
	removeLimited, _ := values["auto_remove_rate_limited_accounts"].(bool)
	if request.RemoveInvalid != nil {
		removeInvalid = *request.RemoveInvalid
	}
	if request.RemoveRateLimited != nil {
		removeLimited = *request.RemoveRateLimited
	}
	if s.accounts == nil {
		return result, ErrStorage
	}
	failures := map[string]error{"accounts": nil}
	if !dryRun {
		if err = s.markStarted(ctx); err != nil {
			return result, err
		}
		defer func() {
			if e := s.markFinished(map[string]any{"accounts": result.TotalRemoved}, failures); err == nil {
				err = e
			}
		}()
	}
	result, err = s.accounts.Cleanup(ctx, removeInvalid, removeLimited, dryRun)
	if err != nil {
		failures["accounts"] = safeError(ctx, err)
		return result, safeError(ctx, err)
	}
	return result, nil
}
func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrInput) {
		return ErrInput
	}
	if errors.Is(err, accounts.ErrBusy) {
		return ErrBusy
	}
	return ErrStorage
}
func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func (s *Service) markStarted(ctx context.Context) error {
	return s.updateState(ctx, map[string]any{"running": true, "last_started_at": timestamp(time.Now())})
}
func (s *Service) markFinished(removed map[string]any, failures map[string]error) error {
	// 请求取消后仍将安全终态写回；关闭会等待这个有界收尾。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errorsByDomain := map[string]any{}
	for domain := range removed {
		errorsByDomain[domain] = nil
		operationErr := failures[domain]
		if operationErr != nil {
			message := ErrStorage.Error()
			if errors.Is(operationErr, context.Canceled) {
				message = "清理已取消"
			}
			if errors.Is(operationErr, context.DeadlineExceeded) {
				message = "清理超时"
			}
			errorsByDomain[domain] = message
		}
	}
	return s.updateState(ctx, map[string]any{"running": false, "last_finished_at": timestamp(time.Now()), "last_removed": removed, "last_error": errorsByDomain})
}

// updateState 只合并本次状态，保留现有表中的未知字段与其他领域结果。
func (s *Service) updateState(parent context.Context, patch map[string]any) error {
	if s.db == nil {
		return ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safeError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('retention_cleanup_state'))"); err != nil {
		return safeError(ctx, err)
	}
	raw := map[string]any{}
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM retention_cleanup_state WHERE id=1 FOR UPDATE").Scan(&data)
	if err == nil {
		if json.Unmarshal(data, &raw) != nil || raw == nil {
			return ErrStorage
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return safeError(ctx, err)
	}
	for key, value := range patch {
		if part, ok := value.(map[string]any); ok {
			base, ok := raw[key].(map[string]any)
			if !ok {
				base = map[string]any{}
			}
			for k, v := range part {
				base[k] = v
			}
			raw[key] = base
		} else {
			raw[key] = value
		}
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return ErrStorage
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO retention_cleanup_state(id,data,revision,updated_at) VALUES(1,$1,1,now()) ON CONFLICT(id) DO UPDATE SET data=excluded.data,revision=retention_cleanup_state.revision+1,updated_at=excluded.updated_at", data); err != nil {
		return safeError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return safeError(ctx, err)
	}
	return nil
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		go func() { s.wg.Wait(); close(s.done) }()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
