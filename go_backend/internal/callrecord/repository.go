// Package callrecord 拥有调用记录；保存有界输入文字与诊断，不保存原图或凭据。
package callrecord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chatgpt2api/go_backend/internal/auth"
)

var ErrStorage = errors.New("调用记录暂不可用")

// Lifecycle 由可观测性领域实现；终态投影与记录共享事务，内存状态只在数据库操作结束后更新。
type Lifecycle interface {
	Prepare(Record) Record
	PersistTerminal(context.Context, *sql.Tx, Record) error
	Started(Record)
	Progress(Record)
	Finished(Record, error)
}

type Repository struct {
	db        *sql.DB
	timeout   time.Duration
	mu        sync.RWMutex
	lifecycle Lifecycle
}

func New(db *sql.DB, timeout time.Duration) *Repository {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Repository{db: db, timeout: timeout}
}
func (r *Repository) SetLifecycle(lifecycle Lifecycle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lifecycle = lifecycle
}
func (r *Repository) observer() Lifecycle { r.mu.RLock(); defer r.mu.RUnlock(); return r.lifecycle }
func (r *Repository) Database() *sql.DB   { return r.db }

// Rejected 只转交入口满载观测，不创建数据库记录或启动调用生命周期。
func (r *Repository) Rejected() {
	if observer, ok := r.observer().(interface{ Rejected() }); ok {
		observer.Rejected()
	}
}

// Progress 只更新有界内存诊断，不修改持久化记录或建立第二条生命周期。
func (r *Repository) Progress(record Record) {
	record.AccountEmail = safeAccountEmail(record.AccountEmail)
	if lifecycle := r.observer(); lifecycle != nil {
		lifecycle.Progress(record)
	}
}

type Record struct {
	ID                                                                string
	Subject                                                           auth.Subject
	Model, Effort, UpstreamModel                                      string
	Started, Ended                                                    time.Time
	AccountID, AccountEmail, ConversationID, MessageID, ResolvedModel string
	Outcome, ErrorCode, Stage                                         string
	PublicError                                                       string
	UpstreamError, UpstreamText, RawError                             string
	APIStatusCode                                                     int
	RequestText                                                       string
	RequestTextTruncated                                              bool
	Endpoint                                                          string
	Attempts, OutputCount                                             int
	Switches                                                          int
	RequestedCount                                                    int
	Timings                                                           map[string]int64
	Trace                                                             *Trace
}

// 邮箱是非关键展示元信息；非法值不能阻断调用终态或污染 VARCHAR(320) 索引。
func safeAccountEmail(value string) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 320 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		value = ""
	}
	return value
}

func (r Record) route() (string, string) {
	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	return endpoint, Business(endpoint)
}

func Business(endpoint string) string {
	switch endpoint {
	case "/v1/chat/completions":
		return "chat"
	case "/v1/responses":
		return "responses"
	case "/v1/messages":
		return "messages"
	case "/v1/search":
		return "search"
	case "/v1/images/generations", "/api/image-tasks/generations":
		return "image_generation"
	case "/v1/images/edits", "/api/image-tasks/edits":
		return "image_edit"
	case "/v1/ppt/generations", "/v1/psd/generations":
		return "file"
	default:
		return "other"
	}
}

func (r Record) payload() []byte {
	ended := ""
	duration := int64(0)
	if !r.Ended.IsZero() {
		ended = r.Ended.UTC().Format(time.RFC3339Nano)
		duration = max(0, r.Ended.Sub(r.Started).Milliseconds())
	}
	endpoint, kind := r.route()
	detail := map[string]any{"key_id": r.Subject.ID, "key_name": r.Subject.Name, "role": r.Subject.Role, "started_at": r.Started.UTC().Format(time.RFC3339Nano), "ended_at": ended, "duration_ms": duration, "status": r.Outcome, "endpoint": endpoint, "business_kind": kind, "attempt_count": r.Attempts, "model": r.Model, "upstream_model": r.UpstreamModel, "resolved_model": r.ResolvedModel, "thinking_effort": r.Effort, "account_id": r.AccountID, "account_email": r.AccountEmail, "conversation_id": r.ConversationID, "message_id": r.MessageID, "failure_code": r.ErrorCode, "stage": r.Stage, "timings_ms": r.Timings}
	detail["switch_count"] = r.Switches
	detail["public_error"] = SafeDiagnosticText(r.PublicError)
	detail["upstream_error"] = CaptureDiagnosticText(r.UpstreamError)
	detail["upstream_text"] = CaptureDiagnosticText(r.UpstreamText)
	detail["raw_error"] = CaptureDiagnosticText(r.RawError)
	detail["api_status_code"] = r.APIStatusCode
	requestText, truncated := CaptureRequestText(r.RequestText)
	detail["request_text_full"] = requestText
	detail["request_text_truncated"] = truncated || r.RequestTextTruncated
	if r.Trace != nil {
		d := r.Trace.Snapshot()
		detail["request_meta"], detail["request_shape"], detail["result_images"] = d.RequestMeta, d.RequestShape, d.ResultImages
		detail["attempts"], detail["metrics"] = d.Attempts, d.Timings
		detail["monitor"] = map[string]any{"events": d.Events, "metrics": d.Timings}
		detail["egress"], detail["status_code"], detail["upstream_request_id"] = d.Egress, d.StatusCode, d.UpstreamRequestID
	}
	if kind == "file" {
		detail["file_count"] = r.OutputCount
	} else {
		detail["image_count"] = r.OutputCount
		detail["image_requested_count"] = r.RequestedCount
	}
	payload := map[string]any{"id": r.ID, "time": r.Started.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05"), "type": "call", "summary": map[string]any{"status": r.Outcome, "model": r.Model, "endpoint": endpoint}, "detail": detail}
	data, _ := json.Marshal(payload)
	return data
}
func (r Record) searchText() string {
	return strings.ToLower(strings.Join([]string{r.ID, r.Model, r.AccountEmail, r.ConversationID}, " "))
}
func (r *Repository) Start(parent context.Context, record Record) error {
	record.AccountEmail = safeAccountEmail(record.AccountEmail)
	if r.db == nil {
		return ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	record.Outcome = "running"
	eventTime := record.Started.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05")
	endpoint, kind := record.route()
	_, err := r.db.ExecContext(ctx, `INSERT INTO call_records(id,event_time,event_day,type,outcome,endpoint,model,account_email,conversation_id,business_kind,search_text,payload,inserted_at) VALUES($1,$2,$3,'call','running',$4,$5,$6,$7,$8,$9,$10,$11)`, record.ID, eventTime, eventTime[:10], endpoint, record.Model, record.AccountEmail, record.ConversationID, kind, record.searchText(), record.payload(), record.Started)
	if err != nil {
		return storageError(ctx, err)
	}
	if observer := r.observer(); observer != nil {
		observer.Started(record)
	}
	return nil
}
func (r *Repository) Finish(parent context.Context, record Record) (finalErr error) {
	record.AccountEmail = safeAccountEmail(record.AccountEmail)
	observer := r.observer()
	if observer != nil {
		record = observer.Prepare(record)
		defer func() { observer.Finished(record, finalErr) }()
	}
	if r.db == nil {
		return ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx, err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE call_records SET outcome=$2,conversation_id=$3,payload=$4,account_email=$5,search_text=$6 WHERE id=$1 AND outcome='running'`, record.ID, record.Outcome, record.ConversationID, record.payload(), record.AccountEmail, record.searchText())
	if err != nil {
		return storageError(ctx, err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrStorage
	}
	if observer != nil {
		if err = observer.PersistTerminal(ctx, tx, record); err != nil {
			return storageError(ctx, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return storageError(ctx, err)
	}
	return nil
}

func storageError(ctx context.Context, _ error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrStorage
}
