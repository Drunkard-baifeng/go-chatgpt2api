package imagetasks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"

	"chatgpt2api/go_backend/internal/images"
)

// BackupSnapshot 不改变任务状态。恢复时已有启动协调会中断活动任务，禁止重放生成。
func (s *Service) BackupSnapshot(ctx context.Context, add func(string, int64, io.Reader) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.closed || s.failed || add == nil {
		return ErrStorage
	}
	data, e := json.Marshal(document{Version: 1, Tasks: s.tasks})
	if e != nil {
		return ErrStorage
	}
	if e = add("data/image_tasks/tasks.json", int64(len(data)), bytes.NewReader(data)); e != nil {
		return e
	}
	ids := make([]string, 0, len(s.tasks))
	for id, t := range s.tasks {
		if t.Status == "queued" || t.Status == "running" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := "spool/" + id + ".json"
		data, e := s.readFile(name, images.MaxRequestBytes)
		if e != nil {
			return ErrStorage
		}
		if e = add("data/image_tasks/"+name, int64(len(data)), bytes.NewReader(data)); e != nil {
			return e
		}
	}
	return nil
}
