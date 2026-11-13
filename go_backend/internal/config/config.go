// Package config 从可选 .env 与进程环境生成唯一启动快照，不改写进程环境。
package config

import (
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Server        Server
	Auth          Auth
	Database      Database
	Upstream      Upstream
	Images        Images
	ImageTasks    ImageTasks
	EditableFiles EditableFiles
}

type Server struct {
	Host                     string   `env:"SERVER_HOST"`
	Port                     int      `env:"SERVER_PORT"`
	AllowedOrigins           []string `env:"SERVER_ALLOWED_ORIGINS"`
	TrustedProxies           []string `env:"SERVER_TRUSTED_PROXIES"`
	ReadHeaderTimeoutSeconds int      `env:"SERVER_READ_HEADER_TIMEOUT_SECONDS"`
	IdleTimeoutSeconds       int      `env:"SERVER_IDLE_TIMEOUT_SECONDS"`
	ShutdownTimeoutSeconds   int      `env:"SERVER_SHUTDOWN_TIMEOUT_SECONDS"`
}

type Auth struct {
	AdminKey string `env:"ADMIN_KEY"`
}

type Database struct {
	Type                         string
	Host                         string `env:"DB_HOST"`
	Port                         int    `env:"DB_PORT"`
	Name                         string `env:"DB_NAME"`
	Username                     string `env:"DB_USER"`
	Password                     string `env:"DB_PASSWORD"`
	SSLMode                      string `env:"DB_SSLMODE"`
	ConnectTimeoutSeconds        int    `env:"DB_CONNECT_TIMEOUT_SECONDS"`
	OperationTimeoutSeconds      int    `env:"DB_OPERATION_TIMEOUT_SECONDS"`
	MaxOpenConnections           int    `env:"DB_MAX_OPEN_CONNECTIONS"`
	MaxIdleConnections           int    `env:"DB_MAX_IDLE_CONNECTIONS"`
	ConnectionMaxLifetimeSeconds int    `env:"DB_CONNECTION_MAX_LIFETIME_SECONDS"`
}

type Upstream struct {
	AccountID              string `env:"UPSTREAM_ACCOUNT_ID"`
	TextTimeoutSeconds     int    `env:"UPSTREAM_TEXT_TIMEOUT_SECONDS"`
	Profile                string `env:"UPSTREAM_PROFILE"`
	TotalTimeoutSeconds    int    `env:"UPSTREAM_TOTAL_TIMEOUT_SECONDS"`
	HeaderTimeoutSeconds   int    `env:"UPSTREAM_HEADER_TIMEOUT_SECONDS"`
	ReadIdleTimeoutSeconds int    `env:"UPSTREAM_READ_IDLE_TIMEOUT_SECONDS"`
	MaxResponseBytes       int64  `env:"UPSTREAM_MAX_RESPONSE_BYTES"`
	ImageStreamSeconds     int    `env:"UPSTREAM_IMAGE_STREAM_SECONDS"`
	ImagePollSeconds       int    `env:"UPSTREAM_IMAGE_POLL_SECONDS"`
}

type Images struct {
	Directory string `env:"IMAGES_DIRECTORY"`
}

type ImageTasks struct {
	Directory string `env:"IMAGE_TASKS_DIRECTORY"`
	Workers   int    `env:"IMAGE_TASKS_WORKERS"`
	Queue     int    `env:"IMAGE_TASKS_QUEUE"`
}

type EditableFiles struct {
	Directory string `env:"EDITABLE_FILES_DIRECTORY"`
	Workers   int    `env:"EDITABLE_FILES_WORKERS"`
	Queue     int    `env:"EDITABLE_FILES_QUEUE"`
}

func defaults() Config {
	return Config{
		Server:        Server{Host: "127.0.0.1", Port: 8100, ReadHeaderTimeoutSeconds: 10, IdleTimeoutSeconds: 120, ShutdownTimeoutSeconds: 15, TrustedProxies: []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}},
		Database:      Database{Type: "postgresql", Host: "127.0.0.1", Port: 5432, SSLMode: "prefer", ConnectTimeoutSeconds: 10, OperationTimeoutSeconds: 30, MaxOpenConnections: 30, MaxIdleConnections: 10, ConnectionMaxLifetimeSeconds: 3600},
		Upstream:      Upstream{Profile: "chrome_146", TextTimeoutSeconds: 300, TotalTimeoutSeconds: 170, HeaderTimeoutSeconds: 30, ReadIdleTimeoutSeconds: 30, MaxResponseBytes: 16 << 20, ImageStreamSeconds: 80, ImagePollSeconds: 60},
		Images:        Images{Directory: "data/images"},
		ImageTasks:    ImageTasks{Directory: "data/image_tasks", Workers: 4, Queue: 32},
		EditableFiles: EditableFiles{Directory: "data/files", Workers: 2, Queue: 16},
	}
}

// Load 不返回包含输入值的解析错误，避免凭据进入日志。
func Load(path string) (Config, error) {
	var zero Config
	values := map[string]string{}
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			if !os.IsNotExist(err) || filepath.Base(path) != ".env" {
				return zero, errors.New("无法读取 .env 文件")
			}
		} else {
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
			if err != nil || len(data) > 1<<20 {
				return zero, errors.New(".env 读取失败或超过 1 MiB")
			}
			values, err = godotenv.Unmarshal(string(data))
			if err != nil {
				return zero, errors.New(".env 语法无效，请检查变量赋值和引号")
			}
		}
	}
	cfg := defaults()
	if err := readEnvironment(reflect.ValueOf(&cfg).Elem(), values); err != nil {
		return zero, err
	}
	if len(values) != 0 {
		return zero, errors.New(".env 包含未知变量，请对照 .env.example 检查名称")
	}
	cfg.Auth.AdminKey = strings.TrimSpace(cfg.Auth.AdminKey)
	if err := cfg.validate(); err != nil {
		return zero, err
	}
	directories := []*string{&cfg.Images.Directory, &cfg.ImageTasks.Directory, &cfg.EditableFiles.Directory}
	for _, directory := range directories {
		resolved := *directory
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		absolute, err := filepath.Abs(resolved)
		if err != nil {
			return zero, errors.New("资产或任务目录无法解析")
		}
		*directory = absolute
	}
	var err error
	canonical := make([]string, len(directories))
	for i, directory := range directories {
		canonical[i], err = canonicalDirectory(*directory)
		if err != nil {
			return zero, err
		}
		for _, previous := range canonical[:i] {
			if directoryContains(previous, canonical[i]) || directoryContains(canonical[i], previous) {
				return zero, errors.New("图片、图片任务与可编辑文件目录不能相同或互相嵌套")
			}
		}
	}
	return cfg, nil
}

// 尚未创建的目录也按最近已存在的父目录解析链接，保持存储所有权隔离。
func canonicalDirectory(directory string) (string, error) {
	ancestor := directory
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", errors.New("图片或任务目录无法解析")
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("图片或任务目录无法解析")
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}
func directoryContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// 同名进程环境优先；文件只生成当前快照，不调用 os.Setenv。
func readEnvironment(value reflect.Value, values map[string]string) error {
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() == reflect.Struct {
			if err := readEnvironment(field, values); err != nil {
				return err
			}
			continue
		}
		key := value.Type().Field(i).Tag.Get("env")
		if key == "" {
			continue
		}
		raw, exists := values[key]
		delete(values, key)
		if environment, ok := os.LookupEnv(key); ok {
			raw, exists = environment, true
		}
		if !exists {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString(raw)
		case reflect.Int, reflect.Int64:
			parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, field.Type().Bits())
			if err != nil {
				return errors.New(key + " 必须为整数")
			}
			field.SetInt(parsed)
		case reflect.Slice:
			items := []string{}
			if strings.TrimSpace(raw) != "" {
				for _, item := range strings.Split(raw, ",") {
					items = append(items, strings.TrimSpace(item))
				}
			}
			field.Set(reflect.ValueOf(items))
		}
	}
	return nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Server.Host) == "" || c.Server.Port < 1 || c.Server.Port > 65535 {
		return errors.New("SERVER_HOST 或 SERVER_PORT 无效")
	}
	if c.Auth.AdminKey == "" {
		return errors.New("ADMIN_KEY 必须填写")
	}
	if c.Database.Type != "postgresql" {
		return errors.New("Go 后端只支持 PostgreSQL")
	}
	if strings.TrimSpace(c.Database.Host) == "" || strings.TrimSpace(c.Database.Name) == "" || strings.TrimSpace(c.Database.Username) == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return errors.New("DB_HOST、DB_NAME、DB_USER 或 DB_PORT 无效")
	}
	switch c.Database.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return errors.New("DB_SSLMODE 无效")
	}
	if c.Database.MaxOpenConnections < 1 || c.Database.MaxIdleConnections < 0 || c.Database.MaxIdleConnections > c.Database.MaxOpenConnections {
		return errors.New("数据库连接池上下限无效")
	}
	for _, seconds := range []int{c.Server.ReadHeaderTimeoutSeconds, c.Server.IdleTimeoutSeconds, c.Server.ShutdownTimeoutSeconds, c.Database.ConnectTimeoutSeconds, c.Database.OperationTimeoutSeconds, c.Database.ConnectionMaxLifetimeSeconds, c.Upstream.TextTimeoutSeconds, c.Upstream.TotalTimeoutSeconds, c.Upstream.HeaderTimeoutSeconds, c.Upstream.ReadIdleTimeoutSeconds} {
		if seconds < 1 || seconds > 86400 {
			return errors.New("超时配置必须在 1 到 86400 秒之间")
		}
	}
	if c.Upstream.Profile == "" || c.Upstream.MaxResponseBytes < 1 || c.Upstream.MaxResponseBytes > 1<<30 {
		return errors.New("上游 profile 或响应字节限制无效")
	}
	if c.Upstream.ImageStreamSeconds < 1 || c.Upstream.ImagePollSeconds < 1 || c.Upstream.ImageStreamSeconds+c.Upstream.ImagePollSeconds > 86370 {
		return errors.New("图片窗口必须为正数且总预算不超过 86400 秒")
	}
	if strings.TrimSpace(c.Images.Directory) == "" {
		return errors.New("图片目录不能为空")
	}
	if strings.TrimSpace(c.ImageTasks.Directory) == "" || c.ImageTasks.Workers < 1 || c.ImageTasks.Workers > 32 || c.ImageTasks.Queue < 1 || c.ImageTasks.Queue > 256 {
		return errors.New("任务目录不能为空，workers 必须为 1–32，queue 必须为 1–256")
	}
	if strings.TrimSpace(c.EditableFiles.Directory) == "" || c.EditableFiles.Workers < 1 || c.EditableFiles.Workers > 8 || c.EditableFiles.Queue < 1 || c.EditableFiles.Queue > 64 {
		return errors.New("可编辑文件目录不能为空，workers 必须为 1–8，queue 必须为 1–64")
	}
	for _, proxy := range c.Server.TrustedProxies {
		if _, err := netip.ParsePrefix(proxy); err != nil {
			if _, err := netip.ParseAddr(proxy); err != nil {
				return errors.New("可信代理必须使用 IP 地址或 CIDR")
			}
		}
	}
	for _, origin := range c.Server.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(origin, "*?#") {
			return errors.New("SERVER_ALLOWED_ORIGINS 必须使用完整 HTTP(S) 来源，不包含通配符、路径、凭据、查询或片段")
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return errors.New("允许来源的端口无效")
			}
		}
	}
	return nil
}
