// Package updates 拥有正式发布查询、受控程序安装与重启任务。
package updates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/filelock"
)

const releasePage = "https://github.com/Drunkard-baifeng/go-chatgpt2api/releases"

var ErrUnsupported = errors.New("当前启动方式不支持在线安装，请通过部署工具升级")
var ErrBusy = errors.New("已有更新正在执行，请等待当前任务结束")

type Options struct {
	Context    context.Context
	ConfigPath string
	Restart    func()
}

type Service struct {
	version, executable, directory, configPath string
	restart                                    func()
	ctx                                        context.Context
	cancel                                     context.CancelFunc
	client                                     *http.Client
	mu                                         sync.Mutex
	worker                                     bool
	handedOff                                  bool
	releaseInstall                             func() error
	done                                       chan struct{}
	cached                                     release
	checked                                    time.Time
	checkError                                 error
}

type event struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Label     string `json:"label"`
	Message   string `json:"message"`
	Tone      string `json:"tone"`
}

type task struct {
	ID         string  `json:"task_id"`
	State      string  `json:"state"`
	Stage      string  `json:"stage"`
	Current    int     `json:"current"`
	Total      int     `json:"total"`
	Label      string  `json:"status_label"`
	Message    string  `json:"message"`
	Tone       string  `json:"tone"`
	Busy       bool    `json:"busy"`
	CurrentTag string  `json:"current_tag"`
	LatestTag  string  `json:"latest_tag"`
	Error      string  `json:"error"`
	Updated    string  `json:"updated_at"`
	Events     []event `json:"events"`
}

func New(version string, options ...Options) *Service {
	o := Options{Context: context.Background()}
	if len(options) != 0 {
		o = options[0]
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	ctx, cancel := context.WithCancel(o.Context)
	executable, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	configPath := ""
	if o.ConfigPath != "" {
		configPath, _ = filepath.Abs(o.ConfigPath)
	}
	return &Service{version: strings.TrimSpace(version), executable: executable, directory: updateDirectory(executable), configPath: configPath, restart: o.Restart, ctx: ctx, cancel: cancel, client: newClient()}
}

func updateDirectory(executable string) string {
	return filepath.Join(filepath.Dir(executable), "."+filepath.Base(executable)+".update")
}

func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.mu.Lock()
	if s.releaseInstall != nil {
		s.releaseInstall()
		s.releaseInstall = nil
	}
	s.mu.Unlock()
	s.client.CloseIdleConnections()
}

func (s *Service) installationReason() string {
	if _, ok := versionParts(s.version); !ok || s.configPath == "" || s.restart == nil || strings.Contains(filepath.ToSlash(s.executable), "/go-build") {
		return "当前为开发或源码启动，请通过部署工具构建或使用正式下载包升级。"
	}
	if runtime.GOOS == "linux" {
		for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
			if _, err := os.Stat(path); err == nil {
				return "容器部署请在部署目录执行：docker compose pull api，然后执行 docker compose up -d api。"
			}
		}
		if os.Getenv("INVOCATION_ID") != "" || os.Getenv("NOTIFY_SOCKET") != "" {
			return "当前由服务管理器托管，请通过服务管理器停止服务、替换发布包并重新启动。"
		}
	}
	if (runtime.GOOS != "windows" && runtime.GOOS != "linux") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "当前系统或架构没有匹配的正式发布包。"
	}
	info, err := os.Lstat(s.executable)
	if err != nil || !info.Mode().IsRegular() {
		return "无法确认当前可执行文件，请通过部署工具升级。"
	}
	return ""
}

func (s *Service) Status(force bool) map[string]any {
	view := map[string]any{"current_tag": s.version, "latest_tag": "", "update_available": false, "release_url": releasePage, "status_label": "尚未检查", "status_message": "", "tone": "warning", "changelog": "", "can_update": false}
	if _, ok := versionParts(s.version); !ok {
		view["status_label"], view["status_message"] = "开发构建", s.installationReason()
		return view
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	r, err := s.check(ctx, force)
	if err != nil {
		view["status_label"], view["status_message"] = "检查失败", err.Error()
		return view
	}
	available := newer(r.Tag, s.version)
	view["latest_tag"], view["release_url"], view["update_available"] = r.Tag, releasePage+"/tag/"+r.Tag, available
	view["changelog"] = "## " + r.Tag + " - " + r.Date + "\n\n" + r.Body
	if available {
		view["status_label"], view["status_message"], view["tone"] = "发现新版本", "发现新版本 "+r.Tag+"，可查看更新记录后安装。", "muted"
	} else {
		view["status_label"], view["status_message"], view["tone"] = "已是最新", "当前版本已是最新稳定版。", "success"
	}
	if reason := s.installationReason(); reason != "" {
		view["status_message"] = reason
	} else if available {
		if _, _, err := r.assets(); err != nil {
			view["status_label"], view["status_message"], view["tone"] = "发布包不完整", err.Error(), "warning"
		} else {
			view["can_update"] = !s.Task()["busy"].(bool)
		}
	}
	return view
}

func (s *Service) check(ctx context.Context, force bool) (release, error) {
	s.mu.Lock()
	ttl := 10 * time.Minute
	if s.checkError != nil {
		ttl = time.Minute
	}
	if !force && !s.checked.IsZero() && time.Since(s.checked) < ttl {
		r, err := s.cached, s.checkError
		s.mu.Unlock()
		return r, err
	}
	s.mu.Unlock()
	r, err := fetchRelease(ctx, s.client)
	s.mu.Lock()
	s.cached, s.checkError, s.checked = r, err, time.Now()
	s.mu.Unlock()
	return r, err
}

func (s *Service) Task() map[string]any {
	t := task{State: "idle", Stage: "idle", Label: "未启动更新", Tone: "info", CurrentTag: s.version, Events: []event{}, Updated: time.Now().UTC().Format(time.RFC3339Nano)}
	var saved task
	if readJSON(filepath.Join(s.directory, "task.json"), &saved) == nil && transactionID.MatchString(saved.ID) {
		t = saved
	}
	if t.Busy {
		s.mu.Lock()
		worker := s.worker
		s.mu.Unlock()
		if !worker {
			if releaseLock, err := filelock.Acquire(filepath.Join(s.directory, "install.lock")); err == nil {
				// 助手或工作者持锁时不能接管其状态；再次读取避免覆盖刚写入的终态。
				if readJSON(filepath.Join(s.directory, "task.json"), &saved) == nil && saved.ID == t.ID && saved.Busy {
					t = saved
					t.State, t.Busy, t.Error = "failed", false, "上次更新被中断，请检查当前版本后重试"
					_ = t.save(s.directory, "failed", "更新中断", t.Error, "danger", t.Current)
				} else if saved.ID == t.ID {
					t = saved
				}
				releaseLock()
			}
		}
	}
	if t.Events == nil {
		t.Events = []event{}
	}
	events := make([]any, len(t.Events))
	for i, item := range t.Events {
		events[i] = item
	}
	return map[string]any{"task_id": t.ID, "state": t.State, "stage": t.Stage, "current": t.Current, "total": t.Total, "status_label": t.Label, "message": t.Message, "tone": t.Tone, "busy": t.Busy, "current_tag": t.CurrentTag, "latest_tag": t.LatestTag, "error": t.Error, "updated_at": t.Updated, "events": events}
}

func (s *Service) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reason := s.installationReason(); reason != "" {
		return fmt.Errorf("%w：%s", ErrUnsupported, reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker {
		return ErrBusy
	}
	if err := s.ctx.Err(); err != nil {
		return errors.New("服务正在关闭，无法受理更新")
	}
	if err := privateDirectory(s.directory); err != nil {
		return errors.New("程序目录不可写，无法创建更新暂存目录")
	}
	releaseLock, err := filelock.Acquire(filepath.Join(s.directory, "install.lock"))
	if err != nil {
		return ErrBusy
	}
	id := randomID()
	t := task{ID: id, State: "queued", Stage: "queued", Total: 6, Label: "等待更新", Message: "更新任务已受理", Tone: "info", Busy: true, CurrentTag: s.version, Events: []event{}}
	if err := t.save(s.directory, "queued", "等待更新", "更新任务已受理", "info", 0); err != nil {
		releaseLock()
		return err
	}
	s.worker, s.done, s.releaseInstall = true, make(chan struct{}), releaseLock
	go func() {
		defer func() {
			s.mu.Lock()
			if !s.handedOff {
				releaseLock()
				s.releaseInstall = nil
			}
			s.worker = false
			close(s.done)
			s.mu.Unlock()
		}()
		job, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
		defer cancel()
		if err := s.prepare(job, &t); err != nil {
			t.State, t.Busy, t.Error = "failed", false, err.Error()
			_ = t.save(s.directory, "failed", "更新失败", err.Error(), "danger", t.Current)
		}
	}()
	return nil
}

func (s *Service) prepare(ctx context.Context, t *task) error {
	t.State = "running"
	if err := t.save(s.directory, "checking", "检查发布", "确认最新稳定版和匹配平台的发布包", "info", 1); err != nil {
		return err
	}
	r, err := s.check(ctx, true)
	if err != nil {
		return err
	}
	if !newer(r.Tag, s.version) {
		return errors.New("没有可安装的更新版本，未修改当前程序")
	}
	archive, manifest, err := r.assets()
	if err != nil {
		return err
	}
	t.LatestTag = r.Tag
	dir := filepath.Join(s.directory, "job-"+t.ID)
	if err := privateDirectory(dir); err != nil {
		return errors.New("无法创建更新任务目录")
	}
	if err := t.save(s.directory, "downloading", "下载更新", "正在下载 "+archive.Name, "info", 2); err != nil {
		return err
	}
	archivePath := filepath.Join(dir, "package")
	if err := download(ctx, s.client, archive, archivePath, maxPackage); err != nil {
		return err
	}
	checksum, err := readRemote(ctx, s.client, manifest.URL, 64<<10)
	if err != nil {
		return err
	}
	if err := verifyDigest(checksum, manifest.Digest); err != nil {
		return errors.New("发布校验清单摘要不匹配，已停止更新")
	}
	if err := t.save(s.directory, "verifying", "校验更新", "核对 SHA256 并检查压缩包内的匹配程序", "info", 3); err != nil {
		return err
	}
	if err := verifyPackage(archivePath, archive.Name, checksum); err != nil {
		return err
	}
	staged := filepath.Join(dir, executableName())
	if err := extractProgram(archivePath, archive.Name, staged); err != nil {
		return err
	}
	if err := verifyProgram(ctx, staged, r.Tag); err != nil {
		return err
	}
	digest, err := fileDigest(staged)
	if err != nil {
		return errors.New("无法读取已校验的更新程序")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return errors.New("无法保留当前工作目录")
	}
	oldDigest, err := fileDigest(s.executable)
	if err != nil {
		return errors.New("无法校验当前程序")
	}
	p := installPlan{ID: t.ID, Target: s.executable, Config: s.configPath, WorkingDirectory: cwd, ParentPID: os.Getpid(), OldTag: s.version, NewTag: r.Tag, Digest: digest, OldDigest: oldDigest, Token: randomID()}
	if err := writeJSON(filepath.Join(dir, "plan.json"), p); err != nil {
		return err
	}
	if err := t.save(s.directory, "installing", "准备安装", "校验通过，正在准备独立安装助手", "info", 4); err != nil {
		return err
	}
	if err := startHelper(ctx, dir, s.executable); err != nil {
		return err
	}
	if err := t.save(s.directory, "restarting", "准备重启", "正在关闭现用服务，安装后将自动重新连接", "info", 5); err != nil {
		return err
	}
	// 状态先落盘，再允许入口排空 HTTP 并释放领域资源。
	if err := writeJSON(filepath.Join(dir, "handoff.json"), map[string]string{"token": p.Token}); err != nil {
		return err
	}
	s.mu.Lock()
	s.handedOff = true
	s.mu.Unlock()
	s.restart()
	return nil
}

func (t *task) save(directory, stage, label, message, tone string, current int) error {
	t.Stage, t.Label, t.Message, t.Tone, t.Current = stage, label, message, tone, current
	t.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	t.Events = append(t.Events, event{ID: fmt.Sprint(len(t.Events) + 1), Timestamp: t.Updated, Label: label, Message: message, Tone: tone})
	if len(t.Events) > 32 {
		t.Events = t.Events[len(t.Events)-32:]
	}
	return writeJSON(filepath.Join(directory, "task.json"), t)
}

func randomID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("无法生成安全随机数")
	}
	return hex.EncodeToString(bytes[:])
}

func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("更新目录无效")
	}
	return nil
}

func readJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
		return errors.New("更新状态不可读")
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, target) != nil {
		return errors.New("更新状态无效")
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("无法编码更新状态")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return errors.New("无法保存更新状态，请检查程序目录权限")
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("无法保存更新状态，请检查程序目录权限")
	}
	// Windows 中状态读取或病毒扫描可能短暂持有旧文件句柄。
	for attempt := 0; attempt < 20; attempt++ {
		if os.Rename(name, path) == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("无法保存更新状态，请检查程序目录权限")
}
