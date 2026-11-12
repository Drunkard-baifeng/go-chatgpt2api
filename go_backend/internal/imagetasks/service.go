package imagetasks

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/filelock"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
)

var ErrFull = errors.New("图片任务队列已满，请稍后重试")
var ErrStorage = errors.New("图片任务存储暂不可用")
var ErrInput = errors.New("图片任务参数无效或当前任务无法续查")
var ErrNotFound = errors.New("图片任务不存在")

type Executor interface {
	ExecuteTask(context.Context, auth.Subject, images.Request, images.TaskOptions, func(context.Context, images.Result) error) (images.Result, error)
}
type job struct {
	id     string
	resume bool
	extra  time.Duration
}
type Service struct {
	mu                  sync.Mutex
	root                *os.Root
	release             func() error
	tasks               map[string]*task
	executor            Executor
	runtime             func(context.Context) (map[string]any, error)
	jobs                chan job
	slots               chan struct{}
	ctx                 context.Context
	cancel              context.CancelFunc
	workers, admissions sync.WaitGroup
	closed              bool
	failed              bool
	closeOnce           sync.Once
	closeDone           chan struct{}
	closeErr            error
}

func New(directory string, workers, queue int, executor Executor, runtime func(context.Context) (map[string]any, error)) (*Service, error) {
	if directory == "" || workers < 1 || workers > 32 || queue < 1 || queue > 256 || executor == nil {
		return nil, ErrInput
	}
	if os.MkdirAll(directory, 0700) != nil {
		return nil, ErrStorage
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return nil, ErrStorage
	}
	if root.MkdirAll("spool", 0700) != nil {
		root.Close()
		return nil, ErrStorage
	}
	info, e := root.Lstat("spool")
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		root.Close()
		return nil, ErrStorage
	}
	release, e := filelock.Acquire(filepath.Join(directory, "writer.lock"))
	if e != nil {
		root.Close()
		return nil, ErrStorage
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{root: root, release: release, executor: executor, runtime: runtime, ctx: ctx, cancel: cancel, jobs: make(chan job, workers+queue), slots: make(chan struct{}, workers+queue), closeDone: make(chan struct{})}
	if e = s.load(); e != nil {
		cancel()
		release()
		root.Close()
		return nil, e
	}
	for i := 0; i < workers; i++ {
		s.workers.Add(1)
		go s.worker()
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				ctx, c := context.WithTimeout(s.ctx, 5*time.Second)
				_ = s.cleanup(ctx)
				c()
			}
		}
	}()
	return s, nil
}
func (s *Service) reserve() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return ErrStorage
	}
	select {
	case s.slots <- struct{}{}:
		s.admissions.Add(1)
		return nil
	default:
		return ErrFull
	}
}
func (s *Service) Submit(parent context.Context, owner auth.Subject, read func(context.Context) (string, images.Request, error)) (Row, error) {
	if owner.ID == "" || len(owner.ID) > 200 || read == nil {
		return Row{}, ErrInput
	}
	if e := s.reserve(); e != nil {
		return Row{}, e
	}
	submitted := false
	defer func() {
		if !submitted {
			<-s.slots
		}
		s.admissions.Done()
	}()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	id, req, e := read(ctx)
	if e != nil {
		return Row{}, e
	}
	if ctx.Err() != nil {
		return Row{}, ctx.Err()
	}
	id = strings.TrimSpace(id)
	if !clientID.MatchString(id) || images.ValidateRequest(req) != nil || req.Format != "url" {
		return Row{}, ErrInput
	}
	if e = s.cleanup(ctx); e != nil {
		return Row{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return Row{}, ErrStorage
	}
	for _, t := range s.tasks {
		if t.Owner.ID == owner.ID && t.ClientID == id {
			return t.view(), nil
		}
	}
	if len(s.tasks) >= maxTasks {
		return Row{}, ErrFull
	}
	taskid, e := newID()
	if e != nil {
		return Row{}, e
	}
	now := time.Now().UTC()
	t := &task{ID: taskid, ClientID: id, Owner: owner, Model: req.Model, Size: req.Size, Quality: req.Quality, OutputFormat: req.OutputFormat, N: req.N, Edit: req.Edit, Created: now, Updated: now, Status: "queued", Progress: diskProgress{Results: []diskOutput{}}}
	data, e := json.Marshal(req)
	if e != nil || len(data) > images.MaxRequestBytes {
		return Row{}, ErrInput
	}
	if e = s.atomicWrite("spool/"+taskid+".json", data); e != nil {
		return Row{}, e
	}
	if e = s.replace(t); e != nil {
		s.root.Remove("spool/" + taskid + ".json")
		return Row{}, e
	}
	s.jobs <- job{id: taskid}
	submitted = true
	return t.view(), nil
}
func (s *Service) List(ctx context.Context, owner auth.Subject, ids []string) (Page, error) {
	p := Page{Items: []Row{}, MissingIDs: []string{}}
	if len(ids) > 200 {
		return p, ErrInput
	}
	if e := s.cleanup(ctx); e != nil {
		return p, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return p, ErrStorage
	}
	if len(ids) > 0 {
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || len(id) > 200 {
				return p, ErrInput
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if t := s.tasks[id]; t != nil && t.Owner.ID == owner.ID {
				p.Items = append(p.Items, t.view())
			} else {
				p.MissingIDs = append(p.MissingIDs, id)
			}
		}
		return p, nil
	}
	for _, t := range s.tasks {
		if t.Owner.ID == owner.ID {
			p.Items = append(p.Items, t.view())
		}
	}
	sort.Slice(p.Items, func(i, j int) bool {
		if s.tasks[p.Items[i].ID].Created.Equal(s.tasks[p.Items[j].ID].Created) {
			return p.Items[i].ID > p.Items[j].ID
		}
		return s.tasks[p.Items[i].ID].Created.After(s.tasks[p.Items[j].ID].Created)
	})
	if len(p.Items) > 100 {
		p.Items = p.Items[:100]
	}
	return p, nil
}
func (s *Service) Resume(ctx context.Context, owner auth.Subject, id string, extra time.Duration) (Row, error) {
	if extra < 5*time.Second || extra > 120*time.Second || !taskID.MatchString(id) {
		return Row{}, ErrInput
	}
	if ctx.Err() != nil {
		return Row{}, ctx.Err()
	}
	if e := s.reserve(); e != nil {
		return Row{}, e
	}
	submitted := false
	defer func() {
		if !submitted {
			<-s.slots
		}
		s.admissions.Done()
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return Row{}, ErrStorage
	}
	t := s.tasks[id]
	if t == nil || t.Owner.ID != owner.ID {
		return Row{}, ErrNotFound
	}
	if !terminal(t.Status) {
		return t.view(), nil
	}
	if !t.resumable() {
		return Row{}, ErrInput
	}
	next := *t
	next.Status = "queued"
	next.ErrorCode = ""
	next.PublicError = ""
	next.Updated = time.Now().UTC()
	if e := s.replace(&next); e != nil {
		return Row{}, e
	}
	s.jobs <- job{id: id, resume: true, extra: extra}
	submitted = true
	return next.view(), nil
}
func (s *Service) worker() {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			s.run(j)
			<-s.slots
		}
	}
}
func (s *Service) run(j job) {
	s.mu.Lock()
	t := s.tasks[j.id]
	if s.closed || s.failed || t == nil {
		s.mu.Unlock()
		return
	}
	next := *t
	next.Status = "running"
	next.Updated = time.Now().UTC()
	if s.replace(&next) != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); _ = s.root.Remove("spool/" + j.id + ".json"); s.mu.Unlock() }()
	var runErr error
	func() {
		defer func() {
			if recover() != nil {
				runErr = ErrStorage
			}
		}()
		req := images.Request{Prompt: "查询已提交图片", Model: t.Model, Size: t.Size, Quality: t.Quality, OutputFormat: t.OutputFormat, N: t.N, Edit: t.Edit, Format: "url"}
		if !j.resume {
			s.mu.Lock()
			data, e := s.readFile("spool/"+j.id+".json", images.MaxRequestBytes)
			s.mu.Unlock()
			if e != nil || json.Unmarshal(data, &req) != nil {
				runErr = ErrStorage
				return
			}
		}
		opts := images.TaskOptions{Progress: t.Progress.execution(), Resume: j.resume, ExtraTimeout: j.extra, Checkpoint: func(progress images.TaskProgress) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			current := s.tasks[j.id]
			if current == nil || terminal(current.Status) {
				return ErrStorage
			}
			persisted := progressToDisk(progress)
			if s.failed || !validProgressAdvance(current.Progress, persisted, current.N) {
				return ErrStorage
			}
			updated := *current
			updated.Progress = persisted
			updated.Updated = time.Now().UTC()
			return s.replace(&updated)
		}}
		_, runErr = s.executor.ExecuteTask(s.ctx, t.Owner, req, opts, func(_ context.Context, _ images.Result) error { return s.finish(j.id, nil) })
	}()
	_ = s.finish(j.id, runErr)
}
func (s *Service) finish(id string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.tasks[id]
	if current == nil {
		return ErrStorage
	}
	if terminal(current.Status) {
		return nil
	}
	t := *current
	t.Updated = time.Now().UTC()
	t.Status = "success"
	t.ErrorCode = ""
	t.PublicError = ""
	if err != nil || len(t.Progress.Results) < t.N {
		t.Status = "failed"
		if len(t.Progress.Results) > 0 {
			t.Status = "partial_success"
		}
		if err == nil {
			err = images.ErrIncomplete
		}
		p := images.PublicError(err)
		t.ErrorCode = p.Code
		t.PublicError = p.Message
		if errors.Is(err, upstream.ErrImageText) && len(t.Progress.Results) == 0 {
			t.Status = "text_review"
		}
		if s.closed || errors.Is(err, context.Canceled) {
			t.ErrorCode = "task_interrupted"
			t.PublicError = "服务已停止，任务已中断；已提交的生成请求不会自动重放"
		}
	}
	return s.replace(&t)
}
func (s *Service) cleanup(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	hours := int64(24)
	if s.runtime != nil {
		v, e := s.runtime(ctx)
		if e != nil {
			return ErrStorage
		}
		if value, ok := v["image_retention_hours"]; ok {
			switch n := value.(type) {
			case float64:
				if n < 1 || n >= float64(math.MaxInt64) || math.Trunc(n) != n {
					return ErrStorage
				}
				hours = int64(n)
			case int:
				hours = int64(n)
			case int64:
				hours = n
			case json.Number:
				var e error
				hours, e = n.Int64()
				if e != nil {
					return ErrStorage
				}
			default:
				return ErrStorage
			}
			if hours < 1 {
				return ErrStorage
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return ErrStorage
	}
	now := time.Now()
	next := map[string]*task{}
	for id, t := range s.tasks {
		if !terminal(t.Status) || t.Updated.After(now) || now.Unix()-t.Updated.Unix() <= hoursToSeconds(hours) {
			next[id] = t
		}
	}
	if len(next) != len(s.tasks) {
		return s.save(next)
	}
	return nil
}
func hoursToSeconds(hours int64) int64 {
	if hours > math.MaxInt64/3600 {
		return math.MaxInt64
	}
	return hours * 3600
}

// Close 有界等待；若执行者未遵守取消，保留写锁直到其真正退出。
// Done 在工作者退出且文件资源关闭后关闭，入口可据此安全关闭执行者依赖。
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
			next := map[string]*task{}
			for id, t := range s.tasks {
				copy := *t
				if !terminal(copy.Status) {
					copy.Status = "failed"
					if len(copy.Progress.Results) > 0 {
						copy.Status = "partial_success"
					}
					copy.ErrorCode = "task_interrupted"
					copy.PublicError = "服务已停止，任务已中断；已提交的生成请求不会自动重放"
					copy.Updated = time.Now().UTC()
				}
				next[id] = &copy
				_ = s.root.Remove("spool/" + id + ".json")
			}
			if s.save(next) != nil {
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
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-timer.C:
		return ErrStorage
	}
}
