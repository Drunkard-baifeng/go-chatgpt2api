package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/app"
	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/backup"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/chat"
	"chatgpt2api/go_backend/internal/cleanup"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/database"
	"chatgpt2api/go_backend/internal/editablefiles"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/imagetasks"
	"chatgpt2api/go_backend/internal/observability"
	"chatgpt2api/go_backend/internal/prompts"
	"chatgpt2api/go_backend/internal/proxy"
	"chatgpt2api/go_backend/internal/remoteimports"
	"chatgpt2api/go_backend/internal/settings"
	"chatgpt2api/go_backend/internal/updates"
	"chatgpt2api/go_backend/internal/upstream"
	"github.com/gin-gonic/gin"
)

// 构建时可通过 -X main.version=... 注入；不会复用 Python 版本号。
var version = "go-dev"

type arguments struct {
	configPath, command string
	offline             bool
}

func parseArguments(args []string, help io.Writer) (arguments, error) {
	result := arguments{command: "serve"}
	flags := flag.NewFlagSet("chatgpt2api", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&result.configPath, "config", "", "显式 YAML 配置文件路径")
	flags.BoolVar(&result.offline, "offline", false, "确认所有写入者已离线，仅用于接管")
	usage := func() {
		fmt.Fprintln(help, "用法：chatgpt2api [serve | db status | db verify | db migrate | db adopt --offline] --config <配置文件>")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return result, flag.ErrHelp
		}
		return result, errors.New("命令参数无效，使用 --help 查看用法")
	}
	rest := flags.Args()
	if len(rest) > 0 {
		result.command = rest[0]
		rest = rest[1:]
		if result.command == "db" {
			if len(rest) == 0 {
				return result, errors.New("db 需要 status、verify、migrate 或 adopt 子命令")
			}
			result.command = "db " + rest[0]
			rest = rest[1:]
		}
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				usage()
				return result, flag.ErrHelp
			}
			return result, errors.New("命令参数无效，使用 --help 查看用法")
		}
		if len(flags.Args()) != 0 {
			return result, errors.New("命令包含多余参数")
		}
	}
	switch result.command {
	case "serve", "db status", "db verify", "db migrate", "db adopt":
	default:
		return result, errors.New("未知命令，使用 --help 查看用法")
	}
	if result.command == "db adopt" && !result.offline {
		return result, errors.New("接管前必须停止全部写入者，并显式传入 --offline")
	}
	if result.offline && result.command != "db adopt" {
		return result, errors.New("--offline 仅用于 db adopt")
	}
	if strings.TrimSpace(result.configPath) == "" {
		return result, errors.New("必须通过 --config 明确指定 YAML 文件")
	}
	return result, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, logger); err != nil && !errors.Is(err, flag.ErrHelp) {
		logger.Error("程序退出", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output, help io.Writer, logger *slog.Logger) error {
	arguments, err := parseArguments(args, help)
	if err != nil {
		return err
	}
	cfg, err := config.Load(arguments.configPath)
	if err != nil {
		return err
	}
	if arguments.command == "serve" {
		// 离线验证 TLS 配置；数据库命令不构造传输资源。
		probe, err := upstream.NewSession(cfg.Upstream, "")
		if err != nil {
			return err
		}
		probe.Close()
	}
	budget := time.Duration(cfg.Database.OperationTimeoutSeconds) * time.Second
	operation, stop := context.WithTimeout(ctx, budget)
	defer stop()
	db, err := database.Open(operation, cfg.Database, arguments.command == "serve" || arguments.command == "db migrate")
	if err != nil {
		return safeDatabaseError(err)
	}
	defer db.Close()
	if arguments.command != "serve" {
		switch arguments.command {
		case "db status":
			status, err := db.Status(operation)
			if err != nil {
				return safeDatabaseError(err)
			}
			return json.NewEncoder(output).Encode(status)
		case "db verify":
			err = db.Verify(operation)
		case "db migrate":
			err = db.Migrate(operation)
		case "db adopt":
			err = db.Adopt(operation)
		}
		if err != nil {
			return safeDatabaseError(err)
		}
		return json.NewEncoder(output).Encode(map[string]string{"status": "ok"})
	}
	if err := db.Verify(operation); err != nil {
		return safeDatabaseError(err)
	}
	stop()
	identity := auth.NewPostgres(cfg.Auth.AdminKey, db.SQL, budget)
	gin.SetMode(gin.ReleaseMode)
	accountRepository := accounts.New(db.Ent, budget)
	defer accountRepository.Close()
	settingsService := settings.New(db.SQL, budget)
	settingsService.ConfigureStartup(map[string]any{"server_host": cfg.Server.Host, "server_port": cfg.Server.Port, "database_host": cfg.Database.Host, "database_port": cfg.Database.Port, "database_name": cfg.Database.Name}, cfg.Images.PublicBaseURL)
	logger = slog.New(settings.NewConsoleHandler(logger.Handler(), settingsService.Runtime))
	slog.SetDefault(logger)
	proxyService := proxy.New(db.SQL, budget)
	accountRepository.Configure(settingsService.Runtime, proxyService, accounts.UpstreamRemote{Policy: cfg.Upstream})
	remoteImportService, err := remoteimports.New(ctx, remoteimports.Options{DB: db.SQL, Accounts: accountRepository, Upstream: cfg.Upstream, ResolveProxy: func(ctx context.Context) (string, error) { return proxyService.Resolve(ctx, nil, nil) }})
	if err != nil {
		return err
	}
	defer remoteImportService.Close()
	records := callrecord.New(db.SQL, budget)
	observabilityService := observability.New(records, accountRepository, settingsService, observability.Options{Version: version, Started: time.Now(), DataDirectory: cfg.Images.Directory})
	textService := chat.New(accountRepository, records, cfg.Upstream, nil)
	initialize, cancelInitialize := context.WithTimeout(ctx, budget)
	promptService, err := prompts.New(initialize, db.SQL, prompts.Options{StorageTimeout: min(budget, 30*time.Second)})
	cancelInitialize()
	if err != nil {
		return err
	}
	defer promptService.Close()
	assets, err := images.NewImageStorageService(cfg.Images.Directory, cfg.Images.PublicBaseURL)
	if err != nil {
		return err
	}
	defer assets.Close()
	assets.ConfigureRuntime(settingsService.Runtime)
	if err := assets.StartGenBoxAuto(2, 32); err != nil {
		return err
	}
	imageService := images.New(accountRepository, records, assets, cfg.Upstream, nil)
	imageService.ConfigureRuntime(settingsService.Runtime)
	tasks, err := imagetasks.New(cfg.ImageTasks.Directory, cfg.ImageTasks.Workers, cfg.ImageTasks.Queue, imageService, settingsService.Runtime)
	if err != nil {
		return err
	}
	defer closeArtifactTasks(tasks, logger)
	files, err := editablefiles.New(ctx, editablefiles.Options{DB: db.SQL, Directory: cfg.EditableFiles.Directory, Workers: cfg.EditableFiles.Workers, Queue: cfg.EditableFiles.Queue, Accounts: accountRepository, Calls: records, Upstream: cfg.Upstream})
	if err != nil {
		return err
	}
	defer closeArtifactTasks(files, logger)
	observabilityService.ConfigureExecution(textService.ExecutionCapacity(), imageService.ExecutionCapacity(), files.ExecutionCapacity(), proxyService.ActiveImageEgress)
	backupService, err := backup.New(ctx, db.SQL, settingsService, backup.Options{Database: cfg.Database, Version: version, StorageTimeout: min(budget, 30*time.Second), Snapshots: map[string]backup.Snapshot{"images": assets.BackupSnapshot, "image_tasks": tasks.BackupSnapshot, "editable_files": files.BackupSnapshot}})
	if err != nil {
		return err
	}
	backupService.Start()
	defer backupService.Close()
	cleanupService := cleanup.New(db.SQL, settingsService, accountRepository, assets, observabilityService)
	if err := cleanupService.Start(ctx); err != nil {
		return err
	}
	defer closeCleanup(cleanupService, logger)
	if err := accountRepository.StartLifecycle(ctx, logger); err != nil {
		return err
	}
	settingsService.ConfigureExecution(settings.ExecutionCapabilities{Backup: true, WebDAV: true, RetentionCleanup: true, BackgroundAccountChecks: true})
	router, err := app.NewRouter(app.Options{Auth: identity, Keys: auth.NewKeys(db.SQL, budget, identity), Settings: settingsService, Proxy: proxyService, Accounts: accountRepository, Text: textService, Images: imageService, Tasks: tasks, Assets: assets, Prompts: promptService, Observability: observabilityService, RemoteImports: remoteImportService, Backup: backupService, EditableFiles: files, Cleanup: cleanupService, Updates: updates.New(version), Version: version, Config: cfg.Server, Logger: logger, Ready: db.SQL.PingContext})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)))
	if err != nil {
		return errors.New("HTTP 监听失败，请检查 YAML 的地址和端口")
	}
	defer listener.Close()
	logger.Info("Go 基础服务启动", "version", version, "address", listener.Addr().String())
	return serveHTTP(ctx, listener, router, cfg.Server, logger)
}

func closeCleanup(service *cleanup.Service, logger *slog.Logger) {
	// 工作仍未退出时不能关闭图库、账号池和数据库。
	if service.Close(context.Background()) != nil {
		logger.Error("清理服务关闭失败")
	}
}

func closeArtifactTasks(tasks interface {
	Close() error
	Done() <-chan struct{}
}, logger *slog.Logger) {
	if tasks.Close() != nil {
		// 任务未真正退出时保留其依赖；错误不包含目录、凭据或原始异常。
		logger.Error("生成任务关闭尚未收口，等待工作者退出")
		<-tasks.Done()
		if tasks.Close() != nil {
			logger.Error("生成任务已退出，最终存储收口失败")
		}
	}
}

func safeDatabaseError(err error) error {
	for _, known := range []error{database.ErrConnection, database.ErrEnvironment, database.ErrNeedsMigration, database.ErrNeedsAdoption, database.ErrUnsupportedSchema, database.ErrNewerSchema} {
		if errors.Is(err, known) {
			return known
		}
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("数据库操作已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("数据库操作超时")
	}
	return errors.New("数据库操作失败，请检查结构、权限和连接状态")
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler, cfg config.Server, logger *slog.Logger) error {
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(cfg.IdleTimeoutSeconds) * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return requests },
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		// 不设置覆盖整个 SSE 生命周期的 WriteTimeout 或全局 TimeoutHandler。
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP 服务意外停止")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		cancelRequests()
		if err != nil {
			server.Close()
		}
		<-result
		if err != nil {
			return errors.New("HTTP 请求排空超时，已取消活动请求")
		}
		return nil
	}
}
