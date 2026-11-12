package editablefiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"chatgpt2api/go_backend/internal/auth"
)

const interruptedMessage = "服务已停止或重启，任务已中断；已提交的生成请求不会自动重放"

type Result struct {
	ConversationID string `json:"conversation_id"`
	PrimaryURL     string `json:"primary_url,omitempty"`
	ZipURL         string `json:"zip_url,omitempty"`
}
type Row struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Status         string  `json:"status"`
	StatusLabel    string  `json:"status_label"`
	StatusTone     string  `json:"status_tone"`
	StatusIcon     string  `json:"status_icon"`
	IsActive       bool    `json:"is_active"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	ElapsedSeconds int64   `json:"elapsed_seconds"`
	CanDownload    bool    `json:"can_download"`
	CanDelete      bool    `json:"can_delete"`
	Result         *Result `json:"result,omitempty"`
	Error          string  `json:"error,omitempty"`
}
type Page struct {
	Items      []Row    `json:"items"`
	MissingIDs []string `json:"missing_ids"`
}
type taskRecord struct {
	Owner, ID, Storage, Status, Kind, Model, CreatedAt, UpdatedAt string
	Created, Updated                                              float64
	Started, Ended                                                sql.NullFloat64
	Result                                                        []byte
	Error, Account                                                sql.NullString
	DeletePending                                                 bool
}

const taskColumns = "owner_id,task_id,storage_id,status,kind,COALESCE(model,''),created_at,updated_at,created_ts,updated_ts,started_ts,ended_ts,result,error,account_email,delete_pending"

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (taskRecord, error) {
	var t taskRecord
	e := row.Scan(&t.Owner, &t.ID, &t.Storage, &t.Status, &t.Kind, &t.Model, &t.CreatedAt, &t.UpdatedAt, &t.Created, &t.Updated, &t.Started, &t.Ended, &t.Result, &t.Error, &t.Account, &t.DeletePending)
	if e != nil {
		return t, e
	}
	if !validClientID(t.ID) || !storageID.MatchString(t.Storage) || (t.Kind != "ppt" && t.Kind != "psd") || len(t.Result) > 4096 {
		return t, ErrStorage
	}
	return t, nil
}
func (s *Service) get(ctx context.Context, owner, id string) (taskRecord, error) {
	t, e := scanTask(s.db.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM editable_file_task WHERE owner_id=$1 AND task_id=$2", owner, id))
	if errors.Is(e, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if e != nil {
		return t, ErrStorage
	}
	return t, nil
}
func taskActive(status string) bool { return status == "queued" || status == "running" }
func (t taskRecord) view() Row {
	status := t.Status
	if !taskActive(status) && status != "success" {
		status = "error"
	}
	label, tone, icon := "生成失败", "danger", "i-lucide-circle-alert"
	switch status {
	case "queued":
		label, tone, icon = "排队中", "muted", "i-lucide-clock"
	case "running":
		label, tone, icon = "生成中", "warning", "i-lucide-loader-circle"
	case "success":
		label, tone, icon = "生成成功", "success", "i-lucide-circle-check"
	}
	ended := float64(time.Now().UnixMilli()) / 1000
	if t.Ended.Valid {
		ended = t.Ended.Float64
	}
	start := t.Created
	if t.Started.Valid {
		start = t.Started.Float64
	}
	elapsed := int64(0)
	if !math.IsNaN(ended-start) && !math.IsInf(ended-start, 0) {
		elapsed = int64(min(86400, max(0, ended-start)))
	}
	r := Row{ID: t.ID, Kind: t.Kind, Status: status, StatusLabel: label, StatusTone: tone, StatusIcon: icon, IsActive: taskActive(status), CreatedAt: publicTime(t.CreatedAt, t.Created), UpdatedAt: publicTime(t.UpdatedAt, t.Updated), ElapsedSeconds: elapsed, CanDelete: !taskActive(status)}
	if status == "success" {
		var result Result
		if json.Unmarshal(t.Result, &result) == nil && validStoredResult(t, result) {
			r.Result = &result
			r.CanDownload = !t.DeletePending
		} else {
			r.Status = "error"
			r.StatusLabel = "文件不可用"
			r.StatusTone = "danger"
			r.StatusIcon = "i-lucide-circle-alert"
			r.Error = "文件结果不可用"
		}
	}
	if r.Status == "error" && r.Error == "" {
		r.Error = "文件生成失败，请检查账号状态后重新创建任务"
		if t.Error.String == "task_interrupted" {
			r.Error = interruptedMessage
		}
		if t.Error.String == "storage_failed" {
			r.Error = "文件保存失败"
		}
	}
	return r
}
func validStoredResult(t taskRecord, r Result) bool {
	if !conversationID.MatchString(r.ConversationID) {
		return false
	}
	prefix := "/files/" + t.Kind + "/" + t.Storage + "/"
	if r.ZipURL != prefix+"materials.zip" {
		return false
	}
	if t.Kind == "psd" {
		return r.PrimaryURL == prefix+"output.psd"
	}
	return r.PrimaryURL == prefix+"output.pptx" || r.PrimaryURL == prefix+"output.ppt"
}
func publicTime(raw string, seconds float64) string {
	if parsed, e := time.Parse(time.RFC3339Nano, raw); e == nil {
		return parsed.UTC().Format(time.RFC3339Nano)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 253402300799 {
		return ""
	}
	return time.UnixMilli(int64(seconds * 1000)).UTC().Format(time.RFC3339Nano)
}
func (s *Service) markInterrupted(ctx context.Context) error {
	return markInterruptedIn(ctx, s.db)
}

type taskExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func markInterruptedIn(ctx context.Context, db taskExecer) error {
	now := time.Now().UTC()
	_, e := db.ExecContext(ctx, "UPDATE editable_file_task SET status='error',error='task_interrupted',updated_at=$1,updated_ts=$2,ended_ts=$2,result=NULL WHERE status IN ('queued','running')", now.Format(time.RFC3339Nano), float64(now.UnixMilli())/1000)
	if e != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) List(parent context.Context, owner auth.Subject, ids []string, limit int) (Page, error) {
	page := Page{Items: []Row{}, MissingIDs: []string{}}
	if owner.ID == "" || len(ids) > 200 || limit < 0 || limit > 200 {
		return page, ErrInput
	}
	if limit == 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return page, ErrStorage
	}
	if len(ids) > 0 {
		seen := map[string]bool{}
		for _, id := range ids {
			if !validClientID(id) {
				return page, ErrInput
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			t, e := s.get(ctx, owner.ID, id)
			if errors.Is(e, ErrNotFound) || t.DeletePending {
				page.MissingIDs = append(page.MissingIDs, id)
				continue
			}
			if e != nil {
				return page, e
			}
			page.Items = append(page.Items, t.view())
		}
		return page, nil
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+taskColumns+" FROM editable_file_task WHERE owner_id=$1 AND delete_pending=false ORDER BY created_ts DESC,task_id DESC LIMIT $2", owner.ID, limit)
	if e != nil {
		return page, ErrStorage
	}
	defer rows.Close()
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			return page, ErrStorage
		}
		page.Items = append(page.Items, t.view())
	}
	if rows.Err() != nil {
		return page, ErrStorage
	}
	return page, nil
}

func (s *Service) Delete(parent context.Context, owner auth.Subject, id string) error {
	if owner.ID == "" || !validClientID(id) {
		return ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStorage
	}
	t, e := s.get(ctx, owner.ID, id)
	if e != nil {
		return e
	}
	if taskActive(t.Status) {
		return ErrConflict
	}
	if _, e = s.db.ExecContext(ctx, "UPDATE editable_file_task SET delete_pending=true WHERE owner_id=$1 AND task_id=$2", owner.ID, id); e != nil {
		return ErrStorage
	}
	return s.deleteFilesAndRow(ctx, t)
}
func (s *Service) deleteFilesAndRow(ctx context.Context, t taskRecord) error {
	if !storageID.MatchString(t.Storage) || (t.Kind != "ppt" && t.Kind != "psd") {
		return ErrStorage
	}
	if e := s.root.RemoveAll(t.Kind + "/" + t.Storage); e != nil {
		return ErrStorage
	}
	if _, e := s.db.ExecContext(ctx, "DELETE FROM editable_file_task WHERE owner_id=$1 AND task_id=$2 AND delete_pending=true", t.Owner, t.ID); e != nil {
		return ErrStorage
	}
	return nil
}
