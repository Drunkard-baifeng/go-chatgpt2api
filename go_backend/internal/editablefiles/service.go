// Package editablefiles owns asynchronous editable-file metadata and artifacts.
package editablefiles

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/filelock"
	"chatgpt2api/go_backend/internal/upstream"
)

type Session interface {
	GenerateEditable(context.Context, string, upstream.EditableRequest) (upstream.EditableResult, error)
	Close()
}
type Options struct {
	DB             *sql.DB
	Directory      string
	Workers, Queue int
	Accounts       accounts.Selector
	Calls          *callrecord.Repository
	Upstream       config.Upstream
	Factory        func(config.Upstream, string) (Session, error)
}
type queuedTask struct {
	owner   auth.Subject
	id      string
	request Request
	inputs  []upstream.ImageInput
}
type Service struct {
	db                  *sql.DB
	root                *os.Root
	release             func() error
	options             Options
	ctx                 context.Context
	cancel              context.CancelFunc
	mu                  sync.Mutex
	closed              bool
	jobs                chan queuedTask
	slots, reading      chan struct{}
	admissions, workers sync.WaitGroup
	closeOnce           sync.Once
	closeDone           chan struct{}
	closeErr            error
}

func (s *Service) ExecutionCapacity() int { return s.options.Workers }

func New(parent context.Context, options Options) (*Service, error) {
	if options.DB == nil || options.Directory == "" || options.Workers < 1 || options.Workers > 8 || options.Queue < 1 || options.Queue > 64 || options.Accounts == nil || options.Calls == nil {
		return nil, ErrInput
	}
	if options.Factory == nil {
		options.Factory = func(p config.Upstream, proxy string) (Session, error) { return upstream.NewSession(p, proxy) }
	}
	if os.MkdirAll(options.Directory, 0700) != nil {
		return nil, ErrStorage
	}
	root, e := os.OpenRoot(options.Directory)
	if e != nil {
		return nil, ErrStorage
	}
	release, e := filelock.Acquire(filepath.Join(options.Directory, "writer.lock"))
	if e != nil {
		root.Close()
		return nil, ErrStorage
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{db: options.DB, root: root, release: release, options: options, ctx: ctx, cancel: cancel, jobs: make(chan queuedTask, options.Workers+options.Queue), slots: make(chan struct{}, options.Workers+options.Queue), reading: make(chan struct{}, options.Workers+options.Queue+8), closeDone: make(chan struct{})}
	fail := func() (*Service, error) {
		cancel()
		release()
		root.Close()
		return nil, ErrStorage
	}
	op, stop := context.WithTimeout(parent, 10*time.Second)
	defer stop()
	for _, dir := range []string{"ppt", "psd", ".tmp"} {
		if root.MkdirAll(dir, 0700) != nil {
			return fail()
		}
		info, e := root.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail()
		}
	}
	if e = s.recover(op); e != nil {
		return fail()
	}
	for i := 0; i < options.Workers; i++ {
		s.workers.Add(1)
		go s.worker()
	}
	return s, nil
}
func randomID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *Service) recover(ctx context.Context) error {
	// The supported deployment has one active process for this directory.
	// Coordinate only the local metadata transition; never pin a shared-pool
	// connection for the worker lifetime or filesystem cleanup.
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(671920486331)"); e != nil {
		return ErrStorage
	}
	if e := markInterruptedIn(ctx, tx); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, "SELECT "+taskColumns+" FROM editable_file_task WHERE delete_pending=true OR status<>'success' LIMIT 10001")
	if e != nil {
		return ErrStorage
	}
	pending := []taskRecord{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			rows.Close()
			return ErrStorage
		}
		pending = append(pending, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(pending) > maxTasks {
		return ErrStorage
	}
	if tx.Commit() != nil {
		return ErrStorage
	}
	for _, t := range pending {
		if t.DeletePending {
			e = s.deleteFilesAndRow(ctx, t)
		} else {
			// Rename precedes the success commit. Reclaim only the random storage
			// namespace owned by a known unsuccessful task, including a previous
			// recovery whose filesystem cleanup was interrupted.
			e = s.root.RemoveAll(t.Kind + "/" + t.Storage)
		}
		if e != nil {
			return e
		}
	}
	if e = s.root.RemoveAll(".tmp"); e != nil {
		return ErrStorage
	}
	if e = s.root.MkdirAll(".tmp", 0700); e != nil {
		return ErrStorage
	}
	return nil
}

// Submit bounds input readers before parsing and reserves finite queued work only
// after owner/client idempotence has been checked. Prompts and references are not
// persisted; process interruption ends tasks instead of replaying submissions.
func (s *Service) Submit(parent context.Context, owner auth.Subject, read func(context.Context) (Request, error)) (Row, error) {
	if owner.ID == "" || len(owner.ID) > 256 || read == nil {
		return Row{}, ErrInput
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Row{}, ErrStorage
	}
	select {
	case s.reading <- struct{}{}:
		s.admissions.Add(1)
	default:
		s.mu.Unlock()
		return Row{}, ErrFull
	}
	s.mu.Unlock()
	defer func() { <-s.reading; s.admissions.Done() }()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	req, e := read(ctx)
	if e != nil {
		return Row{}, e
	}
	inputs, e := validateRequest(req)
	if e != nil {
		return Row{}, e
	}
	if req.ClientTaskID == "" {
		req.ClientTaskID, e = randomID()
		if e != nil {
			return Row{}, e
		}
	}
	req.Images = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Row{}, ErrStorage
	}
	if ctx.Err() != nil {
		return Row{}, ctx.Err()
	}
	existing, e := s.get(ctx, owner.ID, req.ClientTaskID)
	if e == nil {
		if existing.DeletePending {
			return Row{}, ErrConflict
		}
		return existing.view(), nil
	}
	if !errors.Is(e, ErrNotFound) {
		return Row{}, e
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return Row{}, ErrFull
	}
	queued := false
	defer func() {
		if !queued {
			<-s.slots
		}
	}()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Row{}, ErrStorage
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(671920486331)"); e != nil {
		return Row{}, ErrStorage
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM editable_file_task").Scan(&count); e != nil {
		return Row{}, ErrStorage
	}
	if count >= maxTasks {
		return Row{}, ErrFull
	}
	id, e := randomID()
	if e != nil {
		return Row{}, e
	}
	now := time.Now().UTC()
	ts := float64(now.UnixMilli()) / 1000
	t := taskRecord{Owner: owner.ID, ID: req.ClientTaskID, Storage: "asset-" + id, Kind: req.Kind, Model: upstream.EditableModel, Status: "queued", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Created: ts, Updated: ts}
	_, e = tx.ExecContext(ctx, "INSERT INTO editable_file_task(owner_id,task_id,storage_id,status,kind,model,created_at,updated_at,created_ts,updated_ts,delete_pending) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,false)", t.Owner, t.ID, t.Storage, t.Status, t.Kind, t.Model, t.CreatedAt, t.UpdatedAt, t.Created)
	if e != nil {
		return Row{}, ErrStorage
	}
	if tx.Commit() != nil {
		return Row{}, ErrStorage
	}
	s.jobs <- queuedTask{owner: owner, id: t.ID, request: req, inputs: inputs}
	queued = true
	return t.view(), nil
}
func (s *Service) worker() {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.jobs:
			func() { defer func() { <-s.slots }(); s.run(job) }()
		}
	}
}

func (s *Service) run(job queuedTask) {
	ctx, cancel := context.WithTimeout(s.ctx, 1200*time.Second)
	defer cancel()
	// Register terminal cleanup before reading or transitioning admitted work.
	// Even an initial read failure has the trusted owner/task identity needed to
	// end the row without acquiring an account or replaying a generation request.
	t := taskRecord{Owner: job.owner.ID, ID: job.id}
	finish := true
	var record callrecord.Record
	var lease *accounts.Lease
	var runErr error
	started := false
	var output *Result
	defer func() {
		if recover() != nil {
			runErr = ErrStorage
		}
		if finish {
			if e := s.finishTask(t, output, runErr); e != nil {
				runErr = e
			}
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		success := runErr == nil && output != nil
		if lease != nil {
			if e := lease.Finish(cleanup, success, 0, runErr); e != nil && runErr == nil {
				runErr = e
			}
		}
		if started {
			record.Ended = time.Now().UTC()
			record.Outcome = "success"
			record.OutputCount = 2
			if runErr != nil || !success {
				record.Outcome = "failed"
				record.ErrorCode = "editable_generation_failed"
				record.OutputCount = 0
				if ctx.Err() != nil {
					record.ErrorCode = "task_interrupted"
				}
			} else {
				record.Stage = "completed"
			}
			diagnostic := callrecord.AttemptResult{Status: record.Outcome, ErrorCode: record.ErrorCode, Stage: record.Stage, ConversationID: record.ConversationID}
			if runErr != nil {
				diagnostic.PublicError = "可编辑文件生成失败"
				record.PublicError = diagnostic.PublicError
				var failure *upstream.Failure
				if errors.As(runErr, &failure) {
					diagnostic.Stage = failure.Stage
					record.Stage = failure.Stage
				}
			}
			if lease != nil {
				record.Trace.EndAttempt(ctx, diagnostic)
			}
			_ = s.options.Calls.Finish(cleanup, record)
		}
	}()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		runErr = context.Canceled
		return
	}
	current, e := s.get(ctx, job.owner.ID, job.id)
	if e != nil {
		s.mu.Unlock()
		runErr = e
		finish = !errors.Is(e, ErrNotFound)
		return
	}
	t = current
	if t.DeletePending || t.Status != "queued" {
		s.mu.Unlock()
		finish = false
		return
	}
	now := time.Now().UTC()
	transition, e := s.db.ExecContext(ctx, "UPDATE editable_file_task SET status='running',started_ts=$3,updated_at=$4,updated_ts=$3 WHERE owner_id=$1 AND task_id=$2 AND status='queued'", t.Owner, t.ID, float64(now.UnixMilli())/1000, now.Format(time.RFC3339Nano))
	s.mu.Unlock()
	if e != nil {
		runErr = ErrStorage
		return
	}
	if n, e := transition.RowsAffected(); e != nil || n != 1 {
		runErr = ErrStorage
		return
	}
	id, e := randomID()
	if e != nil {
		runErr = e
		return
	}
	record = callrecord.Record{ID: "editable-" + id, Subject: job.owner, Model: t.Kind, UpstreamModel: upstream.EditableModel, Effort: upstream.EditableEffort, Started: now, Endpoint: "/v1/" + t.Kind + "/generations", Stage: "account_selection", RequestedCount: 1, Timings: map[string]int64{}}
	record.RequestText, record.RequestTextTruncated = callrecord.CaptureRequestText(job.request.Prompt)
	record.Trace = callrecord.NewTrace(callrecord.RequestMetadata{}, map[string]int{"images": len(job.inputs)})
	ctx = callrecord.WithTrace(ctx, record.Trace)
	if runErr = s.options.Calls.Start(ctx, record); runErr != nil {
		return
	}
	started = true
	paid, ok := s.options.Accounts.(interface {
		AcquireEditable(context.Context) (*accounts.Lease, error)
	})
	if !ok {
		runErr = accounts.ErrSelection
		return
	}
	selectionStarted := time.Now()
	selectionDone := callrecord.Span(ctx, "account_selection", "account_wait_ms")
	lease, runErr = paid.AcquireEditable(ctx)
	selectionDone(runErr)
	if runErr != nil {
		return
	}
	ctx = record.Trace.BeginAttempt(ctx, 1, 1, lease.Account.ID, lease.Account.Email, "", lease.Account.Proxy, selectionStarted)
	record.AccountID = lease.Account.ID
	record.AccountEmail = lease.Account.Email
	record.Stage = "editable_generation"
	record.Attempts = 1
	s.options.Calls.Progress(record)
	_, runErr = s.db.ExecContext(ctx, "UPDATE editable_file_task SET account_email=$3 WHERE owner_id=$1 AND task_id=$2", t.Owner, t.ID, lease.Account.Email)
	if runErr != nil {
		runErr = ErrStorage
		return
	}
	session, e := s.options.Factory(s.options.Upstream, lease.Account.Proxy)
	if e != nil {
		runErr = e
		return
	}
	defer session.Close()
	result, e := session.GenerateEditable(ctx, lease.Account.AccessToken, upstream.EditableRequest{Kind: t.Kind, Prompt: job.request.Prompt, Inputs: job.inputs, Validate: ValidateArtifact, Progress: func(progress upstream.ConversationResult) error {
		record.ConversationID = progress.ConversationID
		record.Stage = "editable_poll"
		s.options.Calls.Progress(record)
		return nil
	}})
	if e != nil {
		runErr = e
		return
	}
	record.ConversationID = result.ConversationID
	record.MessageID = result.MessageID
	if !conversationID.MatchString(result.ConversationID) || ValidateArtifact(result.Primary.Extension, result.Primary.Data) != nil || ValidateArtifact(result.Archive.Extension, result.Archive.Data) != nil || result.Archive.Extension != "zip" || (t.Kind == "ppt" && result.Primary.Extension != "ppt" && result.Primary.Extension != "pptx") || (t.Kind == "psd" && result.Primary.Extension != "psd") {
		runErr = ErrFormat
		return
	}
	publishDone := callrecord.Span(ctx, "file_publish", "publish_ms")
	record.Stage = "file_publish"
	s.options.Calls.Progress(record)
	output, runErr = s.publish(ctx, t, result)
	publishDone(runErr)
}

func (s *Service) finishTask(t taskRecord, result *Result, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	status, code := "success", ""
	var raw any
	if cause != nil || result == nil {
		status = "error"
		code = "generation_failed"
		if s.closed || errors.Is(cause, context.Canceled) {
			code = "task_interrupted"
		}
		if errors.Is(cause, ErrStorage) {
			code = "storage_failed"
		}
	} else {
		data, e := json.Marshal(result)
		if e != nil || !validStoredResult(t, *result) {
			return ErrStorage
		}
		raw = data
	}
	now := time.Now().UTC()
	res, e := s.db.ExecContext(ctx, "UPDATE editable_file_task SET status=$3,result=$4,error=$5,updated_at=$6,updated_ts=$7,ended_ts=$7 WHERE owner_id=$1 AND task_id=$2 AND status IN ('queued','running') AND delete_pending=false", t.Owner, t.ID, status, raw, code, now.Format(time.RFC3339Nano), float64(now.UnixMilli())/1000)
	if e != nil {
		if result != nil {
			_ = s.root.RemoveAll(t.Kind + "/" + t.Storage)
		}
		return ErrStorage
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return ErrStorage
	}
	return nil
}

func (s *Service) Done() <-chan struct{} { return s.closeDone }
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		go func() {
			s.admissions.Wait()
			s.workers.Wait()
			s.mu.Lock()
			defer s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if s.markInterrupted(ctx) != nil {
				s.closeErr = ErrStorage
			}
			if s.root.Close() != nil {
				s.closeErr = ErrStorage
			}
			if s.release() != nil {
				s.closeErr = ErrStorage
			}
			close(s.closeDone)
		}()
	})
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-timer.C:
		return ErrStorage
	}
}
