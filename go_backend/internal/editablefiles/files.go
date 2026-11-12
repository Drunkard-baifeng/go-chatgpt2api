package editablefiles

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/upstream"
)

func (s *Service) publish(ctx context.Context, t taskRecord, result upstream.EditableResult) (*Result, error) {
	temp := ".tmp/" + t.Storage
	if !storageID.MatchString(t.Storage) || s.root.Mkdir(temp, 0700) != nil {
		return nil, ErrStorage
	}
	defer s.root.RemoveAll(temp)
	primary := "output." + result.Primary.Extension
	for _, file := range []struct {
		name string
		data []byte
	}{{primary, result.Primary.Data}, {"materials.zip", result.Archive.Data}} {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f, e := s.root.OpenFile(temp+"/"+file.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, ErrStorage
		}
		_, e = f.Write(file.data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return nil, ErrStorage
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, context.Canceled
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	current, e := s.get(ctx, t.Owner, t.ID)
	if e != nil || current.DeletePending || current.Status != "running" {
		return nil, ErrStorage
	}
	if s.root.Rename(temp, t.Kind+"/"+t.Storage) != nil {
		return nil, ErrStorage
	}
	prefix := "/files/" + t.Kind + "/" + t.Storage + "/"
	return &Result{ConversationID: result.ConversationID, PrimaryURL: prefix + primary, ZipURL: prefix + "materials.zip"}, nil
}

// Open is public by an unguessable storage ID, but still requires live successful
// metadata. Tombstones deny reads before physical deletion is attempted.
func (s *Service) Open(parent context.Context, relative string) (*os.File, error) {
	if !assetPath.MatchString(relative) {
		return nil, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrStorage
	}
	return s.openLocked(ctx, relative)
}
func (s *Service) openLocked(ctx context.Context, relative string) (*os.File, error) {
	parts := strings.Split(relative, "/")
	if len(parts) != 3 || !assetPath.MatchString(relative) {
		return nil, ErrNotFound
	}
	t, e := scanTask(s.db.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM editable_file_task WHERE storage_id=$1 AND kind=$2 AND status='success' AND delete_pending=false", parts[1], parts[0]))
	if e != nil {
		return nil, ErrNotFound
	}
	view := t.view()
	if !view.CanDownload || view.Result == nil || (view.Result.PrimaryURL != "/files/"+relative && view.Result.ZipURL != "/files/"+relative) {
		return nil, ErrNotFound
	}
	for _, p := range []string{parts[0], parts[0] + "/" + parts[1], relative} {
		info, e := s.root.Lstat(p)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrNotFound
		}
	}
	f, e := s.root.Open(relative)
	if e != nil {
		return nil, ErrNotFound
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > assettransfer.MaxBytes {
		f.Close()
		return nil, ErrNotFound
	}
	return f, nil
}

// BackupSnapshot holds the domain publication/deletion lock while enumerating
// metadata and products. Locks, temporary files and undeclared files are omitted.
func (s *Service) BackupSnapshot(ctx context.Context, add func(string, int64, io.Reader) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.closed || add == nil {
		return ErrStorage
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+taskColumns+" FROM editable_file_task WHERE delete_pending=false ORDER BY owner_id,task_id LIMIT 10001")
	if e != nil {
		return ErrStorage
	}
	tasks := []taskRecord{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			rows.Close()
			return ErrStorage
		}
		tasks = append(tasks, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(tasks) > maxTasks {
		return ErrStorage
	}
	// Raw historical result/error values are excluded from the portable metadata
	// projection, preventing obsolete signed URLs or credential-bearing errors.
	metadata := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		metadata = append(metadata, map[string]any{"owner_id": t.Owner, "task_id": t.ID, "storage_id": t.Storage, "task": t.view()})
	}
	data, e := json.Marshal(metadata)
	if e != nil {
		return ErrStorage
	}
	if e = add("data/files/editable-file-tasks.json", int64(len(data)), bytes.NewReader(data)); e != nil {
		return e
	}
	for _, t := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		view := t.view()
		if !view.CanDownload || view.Result == nil {
			continue
		}
		for _, url := range []string{view.Result.PrimaryURL, view.Result.ZipURL} {
			relative := strings.TrimPrefix(url, "/files/")
			f, e := s.openLocked(ctx, relative)
			if e != nil {
				return e
			}
			info, e := f.Stat()
			if e != nil {
				f.Close()
				return ErrStorage
			}
			e = add("data/files/"+relative, info.Size(), contextReader{ctx: ctx, r: f})
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return ErrStorage
			}
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
