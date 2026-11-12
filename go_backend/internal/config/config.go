// Package config 只从显式 YAML 文件生成启动快照，不读取环境变量覆盖。
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

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Server        Server        `yaml:"server"`
	Auth          Auth          `yaml:"auth"`
	Database      Database      `yaml:"database"`
	Upstream      Upstream      `yaml:"upstream"`
	Images        Images        `yaml:"images"`
	ImageTasks    ImageTasks    `yaml:"image_tasks"`
	EditableFiles EditableFiles `yaml:"editable_files"`
}

type Server struct {
	Host                     string   `yaml:"host"`
	Port                     int      `yaml:"port"`
	AllowedOrigins           []string `yaml:"allowed_origins"`
	TrustedProxies           []string `yaml:"trusted_proxies"`
	ReadHeaderTimeoutSeconds int      `yaml:"read_header_timeout_seconds"`
	IdleTimeoutSeconds       int      `yaml:"idle_timeout_seconds"`
	ShutdownTimeoutSeconds   int      `yaml:"shutdown_timeout_seconds"`
}

type Auth struct {
	AdminKey string `yaml:"admin_key"`
}

type Database struct {
	Type                         string `yaml:"type"`
	Host                         string `yaml:"host"`
	Port                         int    `yaml:"port"`
	Name                         string `yaml:"name"`
	Username                     string `yaml:"username"`
	Password                     string `yaml:"password"`
	SSLMode                      string `yaml:"sslmode"`
	ConnectTimeoutSeconds        int    `yaml:"connect_timeout_seconds"`
	OperationTimeoutSeconds      int    `yaml:"operation_timeout_seconds"`
	MaxOpenConnections           int    `yaml:"max_open_connections"`
	MaxIdleConnections           int    `yaml:"max_idle_connections"`
	ConnectionMaxLifetimeSeconds int    `yaml:"connection_max_lifetime_seconds"`
}

type Upstream struct {
	AccountID              string `yaml:"account_id"`
	TextTimeoutSeconds     int    `yaml:"text_timeout_seconds"`
	Profile                string `yaml:"profile"`
	TotalTimeoutSeconds    int    `yaml:"total_timeout_seconds"`
	HeaderTimeoutSeconds   int    `yaml:"header_timeout_seconds"`
	ReadIdleTimeoutSeconds int    `yaml:"read_idle_timeout_seconds"`
	MaxResponseBytes       int64  `yaml:"max_response_bytes"`
	ImageStreamSeconds     int    `yaml:"image_stream_seconds"`
	ImagePollSeconds       int    `yaml:"image_poll_seconds"`
}

type Images struct {
	Directory     string `yaml:"directory"`
	PublicBaseURL string `yaml:"public_base_url"`
}

type ImageTasks struct {
	Directory string `yaml:"directory"`
	Workers   int    `yaml:"workers"`
	Queue     int    `yaml:"queue"`
}

type EditableFiles struct {
	Directory string `yaml:"directory"`
	Workers   int    `yaml:"workers"`
	Queue     int    `yaml:"queue"`
}

func defaults() Config {
	return Config{
		Server:        Server{Host: "127.0.0.1", Port: 8100, ReadHeaderTimeoutSeconds: 10, IdleTimeoutSeconds: 120, ShutdownTimeoutSeconds: 15},
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
	file, err := os.Open(path)
	if err != nil {
		return zero, errors.New("无法读取 YAML 配置文件，请通过 --config 指定存在的文件")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return zero, errors.New("配置读取失败或文件超过 1 MiB")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return zero, errors.New("YAML 语法无效或配置为空")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return zero, errors.New("配置只能包含一个 YAML 文档")
	}
	if err := validateNode(document.Content[0], reflect.TypeOf(zero)); err != nil {
		return zero, err
	}
	cfg := defaults()
	strict := yaml.NewDecoder(strings.NewReader(string(data)))
	strict.KnownFields(true)
	if err := strict.Decode(&cfg); err != nil {
		return zero, errors.New("YAML 字段、类型或重复键无效")
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

func validateNode(node *yaml.Node, kind reflect.Type) error {
	if node.Kind == yaml.AliasNode || node.Tag == "!!null" {
		return errors.New("配置不接受空值、别名或合并键")
	}
	switch kind.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return errors.New("配置分区必须是映射")
		}
		fields := make(map[string]reflect.Type, kind.NumField())
		for i := 0; i < kind.NumField(); i++ {
			f := kind.Field(i)
			fields[f.Tag.Get("yaml")] = f.Type
		}
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			field, exists := fields[key.Value]
			if key.Tag != "!!str" || !exists || seen[key.Value] {
				return errors.New("配置包含未知字段、重复键或合并键")
			}
			seen[key.Value] = true
			if err := validateNode(value, field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return errors.New("配置列表类型无效")
		}
		for _, item := range node.Content {
			if err := validateNode(item, kind.Elem()); err != nil {
				return err
			}
		}
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return errors.New("文本配置必须是字符串")
		}
	case reflect.Int, reflect.Int64:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return errors.New("数值配置必须使用整数，不能加引号")
		}
	default:
		return errors.New("不支持的配置类型")
	}
	return nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Server.Host) == "" || c.Server.Port < 1 || c.Server.Port > 65535 {
		return errors.New("server 的地址或端口无效")
	}
	if c.Auth.AdminKey == "" {
		return errors.New("auth.admin_key 必须填写")
	}
	if c.Database.Type != "postgresql" {
		return errors.New("Go 后端只支持 PostgreSQL")
	}
	if strings.TrimSpace(c.Database.Host) == "" || strings.TrimSpace(c.Database.Name) == "" || strings.TrimSpace(c.Database.Username) == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return errors.New("database 的地址、名称、用户或端口无效")
	}
	switch c.Database.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return errors.New("database.sslmode 无效")
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
	if c.Images.PublicBaseURL != "" {
		u, err := url.Parse(c.Images.PublicBaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" || u.ForceQuery || strings.Contains(c.Images.PublicBaseURL, "#") {
			return errors.New("图片公开基础地址无效")
		}
		if u.Port() != "" {
			port, e := strconv.Atoi(u.Port())
			if e != nil || port < 1 || port > 65535 {
				return errors.New("图片公开基础地址端口无效")
			}
		}
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
			return errors.New("server.allowed_origins 必须使用完整 HTTP(S) 来源，不包含通配符、路径、凭据、查询或片段")
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
