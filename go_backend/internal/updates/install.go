package updates

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"chatgpt2api/go_backend/internal/filelock"
)

type installPlan struct {
	ID               string `json:"id"`
	Target           string `json:"target"`
	Config           string `json:"config"`
	WorkingDirectory string `json:"working_directory"`
	ParentPID        int    `json:"parent_pid"`
	OldTag           string `json:"old_tag"`
	NewTag           string `json:"new_tag"`
	Digest           string `json:"digest"`
	OldDigest        string `json:"old_digest"`
	Token            string `json:"token"`
}

type readyMarker struct {
	Token   string `json:"token"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

var transactionID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var errChildStillRunning = errors.New("无法确认未就绪的新进程已退出，未恢复程序文件，请检查服务日志")

func readPlan(directory string) (installPlan, error) {
	var p installPlan
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || readJSON(filepath.Join(directory, "plan.json"), &p) != nil {
		return p, errors.New("更新安装计划无效")
	}
	if !transactionID.MatchString(p.ID) || !transactionID.MatchString(p.Token) || !digestPattern.MatchString(p.Digest) || !digestPattern.MatchString(p.OldDigest) || p.ParentPID <= 0 || !newer(p.NewTag, p.OldTag) {
		return p, errors.New("更新安装身份无效")
	}
	if !filepath.IsAbs(p.Target) || !filepath.IsAbs(p.Config) || !filepath.IsAbs(p.WorkingDirectory) || filepath.Clean(p.Target) != p.Target || filepath.Base(directory) != "job-"+p.ID || filepath.Dir(directory) != updateDirectory(p.Target) {
		return p, errors.New("更新安装路径无效")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return p, errors.New("更新安装目录不能通过链接跳转")
	}
	return p, nil
}

func copyExecutable(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPackage {
		return errors.New("当前程序文件无效")
	}
	in, err := os.Open(source)
	if err != nil {
		return errors.New("无法读取当前程序")
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return errors.New("无法准备安装助手")
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, maxPackage+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != info.Size() {
		os.Remove(target)
		return errors.New("安装助手复制失败")
	}
	return nil
}

func helperName() string { return "helper" + filepath.Ext(executableName()) }

func startHelper(ctx context.Context, directory, executable string) error {
	if err := copyExecutable(executable, filepath.Join(directory, helperName())); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(directory, "install.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("无法创建安装助手日志")
	}
	cmd := exec.Command(filepath.Join(directory, helperName()), "--internal-update", directory)
	configureProcess(cmd)
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Start()
	log.Close()
	if err != nil {
		return errors.New("无法启动独立安装助手")
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-wait.Done():
			cmd.Process.Kill()
			return errors.New("安装助手就绪超时，未停止当前服务")
		case <-finished:
			return errors.New("安装助手提前退出，未停止当前服务")
		case <-ticker.C:
			var marker readyMarker
			if readJSON(filepath.Join(directory, "helper-ready.json"), &marker) == nil && marker.PID == cmd.Process.Pid {
				return nil
			}
		}
	}
}

// Apply 只由当前程序复制出的助手执行；等待交接和父进程退出后才修改程序。
func Apply(directory, version string) (result error) {
	p, err := readPlan(directory)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil || filepath.Clean(self) != filepath.Join(directory, helperName()) || version != p.OldTag {
		return errors.New("安装助手身份与当前程序不一致")
	}
	selfDigest, err := fileDigest(self)
	if err != nil || selfDigest != p.OldDigest {
		return errors.New("安装助手校验失败")
	}
	var releaseInstall func() error
	defer func() {
		if releaseInstall == nil {
			releaseInstall, _ = filelock.Acquire(filepath.Join(filepath.Dir(directory), "install.lock"))
		}
		if releaseInstall == nil {
			return
		}
		defer releaseInstall()
		if result == nil {
			return
		}
		var current task
		if readJSON(filepath.Join(filepath.Dir(directory), "task.json"), &current) == nil && current.ID == p.ID && current.Busy {
			current.State, current.Busy, current.Error = "failed", false, result.Error()
			_ = current.save(filepath.Dir(directory), "failed", "更新失败", current.Error, "danger", current.Current)
		}
	}()
	if err := writeJSON(filepath.Join(directory, "helper-ready.json"), readyMarker{Token: p.Token, Version: version, PID: os.Getpid()}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	committed := false
	for !committed {
		select {
		case <-ctx.Done():
			return errors.New("更新交接超时，未修改当前程序")
		case <-ticker.C:
			var handoff struct {
				Token string `json:"token"`
			}
			committed = readJSON(filepath.Join(directory, "handoff.json"), &handoff) == nil && handoff.Token == p.Token
		}
	}
	// 已交接后必须监督到父进程真正退出；其排空预算来自原服务配置，可能超过数分钟。
	// 不能在原服务尚未退出时放弃监督，否则随后退出会留下没有程序运行的部署。
	if err := waitParent(context.Background(), p.ParentPID); err != nil {
		return errors.New("现用进程未退出，未替换程序")
	}
	releaseInstall, err = filelock.Acquire(filepath.Join(filepath.Dir(directory), "install.lock"))
	if err != nil {
		return ErrBusy
	}
	installation, stop := context.WithTimeout(context.Background(), 3*time.Minute)
	defer stop()
	return install(installation, directory, p)
}

func install(ctx context.Context, directory string, p installPlan) error {
	var t task
	if readJSON(filepath.Join(filepath.Dir(directory), "task.json"), &t) != nil || t.ID != p.ID {
		return errors.New("更新任务与安装计划不一致")
	}
	fail := func(message string) error {
		t.State, t.Busy, t.Error = "failed", false, message
		_ = t.save(filepath.Dir(directory), "failed", "更新失败", message, "danger", t.Current)
		return errors.New(message)
	}
	old, err := fileDigest(p.Target)
	if err != nil || old != p.OldDigest {
		return fail("现用程序已变化，已停止安装，请检查程序后手动启动")
	}
	restartOld := func(message string) error {
		// 安装预算耗尽后，恢复操作仍有独立的启动预算。
		t.CurrentTag = p.OldTag
		if launchAndWait(context.Background(), directory, p, p.OldTag) != nil {
			return fail(message + "；旧程序文件保留，但未能自动启动，请检查日志后手动启动")
		}
		return fail(message + "；已重新启动旧程序")
	}
	staged := filepath.Join(directory, executableName())
	actual, err := fileDigest(staged)
	if err != nil || actual != p.Digest {
		return restartOld("安装前程序摘要校验失败，未替换现用文件")
	}
	previous := filepath.Join(directory, "previous"+filepath.Ext(executableName()))
	if _, err := os.Lstat(previous); !errors.Is(err, os.ErrNotExist) {
		return restartOld("旧程序保存位置不可用，已停止安装")
	}
	if err := os.Rename(p.Target, previous); err != nil {
		return restartOld("无法保存旧程序，请检查程序目录权限")
	}
	if err := os.Rename(staged, p.Target); err != nil {
		if os.Rename(previous, p.Target) != nil {
			return fail("安装失败且旧程序恢复失败，请从更新目录恢复 previous 文件")
		}
		return restartOld("安装失败，旧程序文件已恢复")
	}
	if err := launchAndWait(ctx, directory, p, p.NewTag); err == nil {
		t.State, t.Busy, t.CurrentTag = "succeeded", false, p.NewTag
		return t.save(filepath.Dir(directory), "completed", "更新成功", "新版本 "+p.NewTag+" 已启动，配置和数据已保留", "success", t.Total)
	} else if errors.Is(err, errChildStillRunning) {
		return fail(err.Error())
	}
	// 仅恢复可执行文件；数据库和业务文件从不自动降级或回写。
	failed := filepath.Join(directory, "failed"+filepath.Ext(executableName()))
	if os.Rename(p.Target, failed) != nil || os.Rename(previous, p.Target) != nil {
		return fail("新版本启动失败，程序恢复未完成，请手动恢复更新目录中的旧程序")
	}
	return restartOld("新版本启动失败，旧程序文件已恢复")
}

func launchAndWait(parent context.Context, directory string, p installPlan, tag string) error {
	readyPath := filepath.Join(directory, "ready.json")
	if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法准备启动确认")
	}
	log, err := os.OpenFile(filepath.Join(directory, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(p.Target, "--internal-update-child", directory, "serve", "--env-file", p.Config)
	cmd.Dir = p.WorkingDirectory
	configureProcess(cmd)
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Start()
	log.Close()
	if err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			return errors.New("更新后的服务提前退出")
		case <-ctx.Done():
			if err := cmd.Process.Kill(); err != nil {
				select {
				case <-finished:
					return errors.New("更新后的服务提前退出")
				default:
					return errChildStillRunning
				}
			}
			select {
			case <-finished:
				return errors.New("更新后的服务未在时限内就绪")
			case <-time.After(10 * time.Second):
				return errChildStillRunning
			}
		case <-ticker.C:
			var marker readyMarker
			if readJSON(readyPath, &marker) == nil && marker.Token == p.Token && marker.Version == tag && marker.PID == cmd.Process.Pid {
				return nil
			}
		}
	}
}

// ReadyCallback 的确认来自新进程本身，不把旧服务或被占用端口的响应当成更新成功。
func ReadyCallback(directory, version string) (func() error, error) {
	p, err := readPlan(directory)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil || filepath.Clean(self) != p.Target || (version != p.NewTag && version != p.OldTag) {
		return nil, errors.New("更新后启动身份无效")
	}
	return func() error {
		return writeJSON(filepath.Join(directory, "ready.json"), readyMarker{Token: p.Token, Version: version, PID: os.Getpid()})
	}, nil
}
