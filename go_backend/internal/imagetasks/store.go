package imagetasks

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
)

const maxTasks = 10000
const maxMetadata = 32 << 20

var taskID = regexp.MustCompile(`^img-[a-f0-9]{32}$`)
var clientID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)
var spoolName = regexp.MustCompile(`^img-[a-f0-9]{32}\.json(\.img-[a-f0-9]{32}\.tmp)?$`)

type diskOutput struct {
	URL, Path            string
	Width, Height, Bytes int
}
type diskProgress struct {
	Slots     []images.TaskSlot `json:"Slots,omitempty"`
	AccountID string
	Slot      int
	Recovery  upstream.ImageRecovery
	Results   []diskOutput
}
type task struct {
	ID, ClientID                   string
	Owner                          auth.Subject
	Model, Size, Quality           string
	OutputFormat                   string `json:"OutputFormat,omitempty"`
	N                              int
	Edit                           bool
	Created, Updated               time.Time
	Status, ErrorCode, PublicError string
	Progress                       diskProgress
}
type document struct {
	Version int              `json:"version"`
	Tasks   map[string]*task `json:"tasks"`
}

func newID() (string, error) {
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return "", ErrStorage
	}
	return "img-" + hex.EncodeToString(id[:]), nil
}
func (s *Service) atomicWrite(name string, data []byte) error {
	id, e := newID()
	if e != nil {
		return e
	}
	tmp := name + "." + id + ".tmp"
	f, e := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrStorage
	}
	defer s.root.Remove(tmp)
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return ErrStorage
	}
	if s.root.Rename(tmp, name) != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) save(tasks map[string]*task) error {
	if len(tasks) > maxTasks {
		return ErrFull
	}
	b, e := json.Marshal(document{Version: 1, Tasks: tasks})
	if e != nil || len(b) > maxMetadata {
		s.failed = true
		return ErrStorage
	}
	if e = s.atomicWrite("tasks.json", b); e != nil {
		s.failed = true
		return e
	}
	s.tasks = tasks
	return nil
}
func (s *Service) replace(t *task) error {
	next := make(map[string]*task, len(s.tasks)+1)
	for id, t := range s.tasks {
		next[id] = t
	}
	next[t.ID] = t
	return s.save(next)
}
func (s *Service) readFile(name string, limit int64) ([]byte, error) {
	i, e := s.root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Size() > limit {
		return nil, ErrStorage
	}
	f, e := s.root.Open(name)
	if e != nil {
		return nil, ErrStorage
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrStorage
	}
	return b, nil
}
func (s *Service) load() error {
	s.tasks = map[string]*task{}
	b, e := s.readFile("tasks.json", maxMetadata)
	if e == nil {
		var d document
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if dec.Decode(&d) != nil || d.Version != 1 || d.Tasks == nil || len(d.Tasks) > maxTasks {
			return ErrStorage
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return ErrStorage
		}
		seen := map[string]bool{}
		for id, t := range d.Tasks {
			if t == nil || id != t.ID || !taskID.MatchString(id) || !clientID.MatchString(t.ClientID) || t.Owner.ID == "" || len(t.Owner.ID) > 200 || t.N < 1 || t.N > images.MaxN || t.Created.IsZero() || t.Updated.Before(t.Created) || len(t.Progress.Results) > t.N || len(t.Progress.AccountID) > 256 {
				return ErrStorage
			}
			if _, ok := map[string]bool{"queued": true, "running": true, "success": true, "partial_success": true, "failed": true, "text_review": true}[t.Status]; !ok {
				return ErrStorage
			}
			if !validProgress(t.Progress, t.N) {
				return ErrStorage
			}
			r := images.Request{Prompt: "校验任务", Model: t.Model, Size: t.Size, Quality: t.Quality, OutputFormat: t.OutputFormat, N: t.N, Format: "url"}
			if images.ValidateRequest(r) != nil {
				return ErrStorage
			}
			key := t.Owner.ID + "\x00" + t.ClientID
			if seen[key] {
				return ErrStorage
			}
			seen[key] = true
			for _, a := range t.Progress.Results {
				if a.URL == "" || a.Width < 1 || a.Height < 1 || a.Bytes < 0 || len(a.Path) > 256 || len(a.URL) > 8192 {
					return ErrStorage
				}
			}
			if !terminal(t.Status) {
				t.Status = "failed"
				if len(t.Progress.Results) > 0 {
					t.Status = "partial_success"
				}
				t.ErrorCode = "task_interrupted"
				t.PublicError = "服务已重启，任务已中断；已提交的生成请求不会自动重放"
				t.Updated = time.Now().UTC()
			}
		}
		s.tasks = d.Tasks
	} else if !errors.Is(e, os.ErrNotExist) {
		return ErrStorage
	}
	// spool 仅存放本队列随机名称的私有活动请求；重启不重放。
	f, e := s.root.Open("spool")
	if e != nil {
		return ErrStorage
	}
	entries, e := f.ReadDir(maxTasks + 1)
	f.Close()
	if e != nil && e != io.EOF {
		return ErrStorage
	}
	if len(entries) > maxTasks {
		return ErrStorage
	}
	for _, entry := range entries {
		name := entry.Name()
		if spoolName.MatchString(name) {
			if s.root.Remove("spool/"+name) != nil {
				return ErrStorage
			}
		}
	}
	return s.save(s.tasks)
}
func progressToDisk(p images.TaskProgress) diskProgress {
	d := diskProgress{AccountID: p.AccountID, Slot: p.Slot, Recovery: p.Recovery, Results: []diskOutput{}}
	d.Slots = cloneSlots(p.Slots)
	d.Recovery.Excluded = append([]string{}, p.Recovery.Excluded...)
	for _, a := range p.Results {
		d.Results = append(d.Results, diskOutput{URL: a.URL, Path: a.Path, Width: a.Width, Height: a.Height, Bytes: a.Bytes})
	}
	return d
}
func (p diskProgress) execution() images.TaskProgress {
	r := images.TaskProgress{AccountID: p.AccountID, Slot: p.Slot, Recovery: p.Recovery, Results: []images.Output{}}
	r.Slots = cloneSlots(p.Slots)
	r.Recovery.Excluded = append([]string{}, p.Recovery.Excluded...)
	for _, a := range p.Results {
		r.Results = append(r.Results, images.Output{URL: a.URL, Path: a.Path, Width: a.Width, Height: a.Height, Bytes: a.Bytes})
	}
	return r
}

func cloneSlots(slots []images.TaskSlot) []images.TaskSlot {
	result := append([]images.TaskSlot(nil), slots...)
	for i := range result {
		result[i].Recovery.Excluded = append([]string{}, result[i].Recovery.Excluded...)
	}
	return result
}

func validProgress(p diskProgress, n int) bool {
	if p.Slot < 0 || p.Slot >= n || len(p.AccountID) > 256 || len(p.Results) > n || (p.Recovery.ConversationID != "" && !p.Recovery.Valid()) {
		return false
	}
	if len(p.Slots) == 0 {
		return true
	}
	if len(p.Slots) != n {
		return false
	}
	completed := 0
	for _, slot := range p.Slots {
		if len(slot.AccountID) > 256 || (slot.Recovery.ConversationID != "" && (slot.AccountID == "" || !slot.Recovery.Valid())) {
			return false
		}
		if slot.Completed {
			if slot.AccountID == "" || slot.Recovery.ConversationID != "" {
				return false
			}
			completed++
		}
	}
	return completed == len(p.Results)
}

func validProgressAdvance(previous, next diskProgress, n int) bool {
	if !validProgress(next, n) || next.AccountID == "" || len(next.Results) < len(previous.Results) {
		return false
	}
	for i, output := range previous.Results {
		if next.Results[i] != output {
			return false
		}
	}
	if len(previous.Slots) > 0 {
		if len(next.Slots) != len(previous.Slots) {
			return false
		}
		for i, slot := range previous.Slots {
			updated := next.Slots[i]
			if slot.Completed && (!updated.Completed || updated.AccountID != slot.AccountID) {
				return false
			}
			if slot.Recovery.Valid() && updated.AccountID != slot.AccountID {
				return false
			}
		}
	} else if previous.Recovery.Valid() && next.AccountID != previous.AccountID {
		return false
	}
	return true
}
