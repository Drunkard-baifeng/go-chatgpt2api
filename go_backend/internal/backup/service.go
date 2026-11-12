package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/settings"
)

type Options struct {
	Database       config.Database
	Version        string
	PGDumpPath     string
	Snapshots      map[string]Snapshot
	Timeout        time.Duration
	StorageTimeout time.Duration
}
type State struct {
	Running        bool    `json:"running"`
	LastStatus     string  `json:"last_status"`
	LastStartedAt  *string `json:"last_started_at"`
	LastFinishedAt *string `json:"last_finished_at"`
	LastError      *string `json:"last_error"`
	LastObjectKey  *string `json:"last_object_key"`
}
type RunResult struct {
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	Encrypted bool   `json:"encrypted"`
}
type TestResult struct {
	OK     bool `json:"ok"`
	Status int  `json:"status,omitempty"`
}
type View struct {
	Items []Item `json:"items"`
	State State  `json:"state"`
}
type Service struct {
	db                      *sql.DB
	settings                *settings.Service
	database                config.Database
	version, pgDumpPath     string
	snapshots               map[string]Snapshot
	timeout, storageTimeout time.Duration
	mu                      sync.Mutex
	closed, started         bool
	lifetime                context.Context
	cancel                  context.CancelFunc
	active                  sync.WaitGroup
	runGate                 chan struct{}
	operations              chan struct{}
	dump                    func(context.Context, string) error
	client                  func(backupConfig) (*r2Client, error)
}

func New(ctx context.Context, db *sql.DB, settingService *settings.Service, options Options) (*Service, error) {
	if db == nil || settingService == nil || options.Database.Type != "postgresql" || options.Database.Host == "" || options.Database.Name == "" || options.Database.Username == "" || options.Database.Port < 1 {
		return nil, ErrInput
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Minute
	}
	if options.Timeout > 10*time.Minute {
		return nil, ErrInput
	}
	if options.StorageTimeout <= 0 {
		options.StorageTimeout = 10 * time.Second
	}
	if options.StorageTimeout > 30*time.Second {
		return nil, ErrInput
	}
	lifetime, cancel := context.WithCancel(context.Background())
	s := &Service{db: db, settings: settingService, database: options.Database, version: options.Version, pgDumpPath: options.PGDumpPath, snapshots: map[string]Snapshot{}, timeout: options.Timeout, storageTimeout: options.StorageTimeout, lifetime: lifetime, cancel: cancel, runGate: make(chan struct{}, 1), operations: make(chan struct{}, 4), client: newR2}
	for domain, snapshot := range options.Snapshots {
		if domain != "images" && domain != "image_tasks" && domain != "editable_files" {
			cancel()
			return nil, ErrInput
		}
		s.snapshots[domain] = snapshot
	}
	s.dump = s.dumpPostgres
	state, err := s.Status(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	if state.LastStatus == "running" {
		state.Running = false
		state.LastStatus = "error"
		state.LastFinishedAt = stringPointer(isoNow())
		state.LastError = stringPointer("备份执行被进程重启中断")
		if err = s.saveState(ctx, state); err != nil {
			cancel()
			return nil, err
		}
	}
	return s, nil
}
func isoNow() string                     { return time.Now().UTC().Format(time.RFC3339) }
func stringPointer(value string) *string { return &value }
func (s *Service) operation(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	select {
	case s.operations <- struct{}{}:
	default:
		s.mu.Unlock()
		return nil, nil, ErrBusy
	}
	s.active.Add(1)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	stop := context.AfterFunc(s.lifetime, cancel)
	return ctx, func() { stop(); cancel(); <-s.operations; s.active.Done() }, nil
}
func (s *Service) configuration(ctx context.Context) (backupConfig, error) {
	runtime, err := s.settings.Runtime(ctx)
	if err != nil {
		return backupConfig{}, ErrStorage
	}
	raw, ok := runtime["backup"].(map[string]any)
	if !ok {
		return backupConfig{}, ErrInput
	}
	get := func(name string) string { value, _ := raw[name].(string); return strings.TrimSpace(value) }
	boolean := func(name string) bool { value, _ := raw[name].(bool); return value }
	number := func(name string, fallback int) int {
		value, ok := raw[name].(float64)
		if !ok {
			return fallback
		}
		if value < 0 || value > 2147483647 {
			return fallback
		}
		return int(value)
	}
	cfg := backupConfig{Enabled: boolean("enabled"), AccountID: get("account_id"), AccessKeyID: get("access_key_id"), SecretAccessKey: get("secret_access_key"), Bucket: get("bucket"), Prefix: get("prefix"), Encrypt: boolean("encrypt"), Passphrase: get("passphrase"), IntervalMinutes: number("interval_minutes", 360), RotationKeep: number("rotation_keep", 10), Include: map[string]bool{}}
	if include, ok := raw["include"].(map[string]any); ok {
		for _, domain := range []string{"images", "image_tasks", "editable_files"} {
			value, _ := include[domain].(bool)
			cfg.Include[domain] = value
		}
	}
	if cfg.IntervalMinutes < 1 {
		cfg.IntervalMinutes = 360
	}
	return cfg, nil
}
func (s *Service) Status(parent context.Context) (State, error) {
	ctx, cancel := context.WithTimeout(parent, s.storageTimeout)
	defer cancel()
	state := State{LastStatus: "idle"}
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM backup_execution_state WHERE id=1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, sqlError(ctx)
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &state) != nil {
		return State{}, ErrStorage
	}
	switch state.LastStatus {
	case "idle", "running", "success", "error":
	default:
		return State{}, ErrStorage
	}
	state.Running = state.LastStatus == "running"
	if state.LastError != nil && *state.LastError != "备份执行被进程重启中断" {
		state.LastError = stringPointer("备份执行失败，请检查配置、工具和存储连接")
	}
	return state, nil
}
func (s *Service) saveState(parent context.Context, state State) error {
	state.Running = false
	data, err := json.Marshal(state)
	if err != nil {
		return ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, s.storageTimeout)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `INSERT INTO backup_execution_state(id,data,revision,updated_at) VALUES(1,$1::jsonb,1,now()) ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data,revision=backup_execution_state.revision+1,updated_at=now()`, data)
	if err != nil {
		return sqlError(ctx)
	}
	return nil
}
func sqlError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrStorage
}
func (s *Service) Test(parent context.Context) (TestResult, error) {
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return TestResult{}, err
	}
	defer finish()
	cfg, err := s.configuration(ctx)
	if err != nil {
		return TestResult{}, err
	}
	client, err := s.client(cfg)
	if err != nil {
		return TestResult{}, err
	}
	status, err := client.Test(ctx)
	if err != nil {
		return TestResult{}, err
	}
	return TestResult{OK: true, Status: status}, nil
}
func (s *Service) List(parent context.Context) (View, error) {
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return View{}, err
	}
	defer finish()
	state, err := s.Status(ctx)
	if err != nil {
		return View{}, err
	}
	cfg, err := s.configuration(ctx)
	if err != nil {
		return View{}, err
	}
	if cfg.AccountID == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || cfg.Bucket == "" {
		return View{Items: []Item{}, State: state}, nil
	}
	client, err := s.client(cfg)
	if err != nil {
		return View{}, err
	}
	items, err := client.List(ctx)
	if err != nil {
		return View{}, err
	}
	return View{Items: items, State: state}, nil
}
func (s *Service) Delete(parent context.Context, key string) error {
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return err
	}
	defer finish()
	select {
	case s.runGate <- struct{}{}:
		defer func() { <-s.runGate }()
	default:
		return ErrBusy
	}
	cfg, err := s.configuration(ctx)
	if err != nil {
		return err
	}
	client, err := s.client(cfg)
	if err != nil {
		return err
	}
	return client.Delete(ctx, strings.TrimSpace(key))
}
func (s *Service) Run(parent context.Context, trigger string) (result RunResult, runErr error) {
	if trigger != "manual" && trigger != "schedule" {
		return result, ErrInput
	}
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return result, err
	}
	defer finish()
	select {
	case s.runGate <- struct{}{}:
		defer func() { <-s.runGate }()
	default:
		return result, ErrBusy
	}
	// The supported deployment has one active process. runGate spans snapshot,
	// upload and rotation; database operations remain short and release the shared
	// pool before an owner snapshot or remote request needs it again.
	previous, err := s.Status(ctx)
	if err != nil {
		return result, err
	}
	started := isoNow()
	running := previous
	running.LastStartedAt = &started
	running.LastStatus = "running"
	running.LastError = nil
	if err = s.saveState(ctx, running); err != nil {
		return result, err
	}
	defer func() {
		if recover() != nil {
			runErr = ErrDump
		}
		terminal := running
		terminal.LastFinishedAt = stringPointer(isoNow())
		terminal.LastStatus = "success"
		terminal.LastObjectKey = &result.Key
		if runErr != nil {
			terminal.LastStatus = "error"
			terminal.LastObjectKey = previous.LastObjectKey
			terminal.LastError = stringPointer(publicError(runErr))
		}
		cleanup, cancel := context.WithTimeout(context.Background(), s.storageTimeout)
		defer cancel()
		if err := s.saveState(cleanup, terminal); err != nil {
			runErr = ErrStorage
		}
	}()
	cfg, err := s.configuration(ctx)
	if err != nil {
		return result, err
	}
	client, err := s.client(cfg)
	if err != nil {
		return result, err
	}
	if cfg.Encrypt && cfg.Passphrase == "" {
		return result, ErrInput
	}
	directory, err := os.MkdirTemp("", "chatgpt2api-backup-")
	if err != nil {
		return result, ErrDump
	}
	defer os.RemoveAll(directory)
	archive, err := s.archive(ctx, cfg, trigger, directory)
	if err != nil {
		return result, err
	}
	suffix := ".tar.gz"
	if cfg.Encrypt {
		source, err := os.Open(archive)
		if err != nil {
			return result, ErrDump
		}
		defer source.Close()
		encrypted := filepath.Join(directory, "backup.tar.gz.enc")
		destination, err := os.OpenFile(encrypted, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return result, ErrDump
		}
		err = encrypt(ctx, &limitedWriter{writer: destination, remaining: maxBackupBytes}, source, cfg.Passphrase)
		closeErr := destination.Close()
		if err != nil {
			return result, err
		}
		if closeErr != nil {
			return result, ErrDump
		}
		archive = encrypted
		suffix += ".enc"
	}
	file, err := os.Open(archive)
	if err != nil {
		return result, ErrDump
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, ErrDump
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, ErrDump
	}
	key := client.config.Prefix + "/backup-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:]) + suffix
	if err = client.Upload(ctx, key, file, info.Size(), cfg.Encrypt, trigger); err != nil {
		return result, err
	}
	result = RunResult{Key: key, Size: info.Size(), Encrypted: cfg.Encrypt}
	if err = client.Rotate(ctx, cfg.RotationKeep, key); err != nil {
		return result, err
	}
	return result, nil
}
func publicError(err error) string {
	for _, known := range []error{ErrInput, ErrRemote, ErrLimit, ErrSnapshot, ErrDumpMissing, ErrDump, ErrStorage} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "备份执行已取消或超时"
	}
	return "备份执行失败"
}
func (s *Service) RunScheduledIfDue(ctx context.Context) error {
	cfg, err := s.configuration(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	state, err := s.Status(ctx)
	if err != nil {
		return err
	}
	if state.Running {
		return nil
	}
	if state.LastFinishedAt != nil {
		finished, err := time.Parse(time.RFC3339, *state.LastFinishedAt)
		if err == nil && time.Since(finished) < time.Duration(cfg.IntervalMinutes)*time.Minute {
			return nil
		}
	}
	_, err = s.Run(ctx, "schedule")
	return err
}
func (s *Service) Start() {
	s.mu.Lock()
	if s.closed || s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.active.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.active.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			_ = s.RunScheduledIfDue(s.lifetime)
			select {
			case <-s.lifetime.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *Service) Close() { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock(); s.active.Wait() }
