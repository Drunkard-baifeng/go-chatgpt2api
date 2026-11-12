// Package settings 拥有业务设置的规范、持久化与安全投影。
package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/managedhttp"
)

var (
	ErrInput    = errors.New("设置参数无效，请检查字段和取值范围")
	ErrConflict = errors.New("设置已被其他操作更新，请刷新后重试")
	ErrStorage  = errors.New("设置存储暂不可用")
)

// specification.json 一次性提取自 Python 字段规范；运行时不依赖 Python。
//
//go:embed specification.json
var specification []byte

type fieldSpec struct {
	Default         any      `json:"default"`
	Min             *float64 `json:"min"`
	Max             *float64 `json:"max"`
	Options         []string `json:"options"`
	Unit            any      `json:"unit"`
	ReadOnly        bool     `json:"read_only"`
	RestartRequired bool     `json:"restart_required"`
	Sensitive       bool     `json:"sensitive"`
	Kind            string   `json:"kind"`
}

var specs = func() map[string]fieldSpec {
	var v map[string]fieldSpec
	if json.Unmarshal(specification, &v) != nil {
		panic("设置字段规范无效")
	}
	return v
}()

type Service struct {
	db            *sql.DB
	timeout       time.Duration
	mu            sync.RWMutex
	startup       map[string]any
	publicBaseURL string
	execution     ExecutionCapabilities
}

// ExecutionCapabilities 由入口按实际安装的执行器设置，业务设置不能伪造运行能力。
type ExecutionCapabilities struct {
	Backup                  bool
	WebDAV                  bool
	RetentionCleanup        bool
	BackgroundAccountChecks bool
}

func (s *Service) ConfigureExecution(capabilities ExecutionCapabilities) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution = capabilities
}

func New(db *sql.DB, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Service{db: db, timeout: timeout, startup: map[string]any{"config_path": "", "server_host": "", "server_port": 0, "database_type": "postgresql", "database_host": nil, "database_port": nil, "database_name": nil, "thread_tokens": 0, "restart_required": true}}
}

// ConfigureStartup 仅接收启动配置所有者提供的公开白名单；在注册路由前调用。
func (s *Service) ConfigureStartup(public map[string]any, baseURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.startup {
		if v, ok := public[key]; ok {
			s.startup[key] = v
		}
	}
	s.publicBaseURL = strings.TrimRight(baseURL, "/")
}

func (s *Service) load(ctx context.Context) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM system_settings WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	return decode(data)
}
func decode(data []byte) (map[string]any, error) {
	v := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil || v == nil {
		return nil, ErrStorage
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return nil, ErrStorage
	}
	return v, nil
}

func (s *Service) View(ctx context.Context) (map[string]any, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return s.project(raw), nil
}

// Runtime 返回执行用的未脱敏设置；调用方不得将它作为 HTTP 响应或日志。
func (s *Service) Runtime(ctx context.Context) (map[string]any, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	values := s.values(raw, false)
	merge(raw, values)
	return raw, nil
}
func (s *Service) ThirdPartyApps(ctx context.Context) (map[string]any, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	v := s.values(raw, true)
	return map[string]any{"api_base_url": v["base_url"], "console_request_timeout_secs": v["console_request_timeout_secs"], "third_party_apps": v["third_party_apps"]}, nil
}
func (s *Service) values(raw map[string]any, mask bool) map[string]any {
	result := map[string]any{}
	for path, spec := range specs {
		value, _ := get(raw, path)
		value = normalizeStored(value, spec)
		if valueText, ok := value.(string); ok {
			value = strings.TrimSpace(valueText)
		}
		if path == "base_url" {
			value, _ = normalizePublicBaseURL(value.(string))
		}
		if mask && spec.Sensitive {
			set(result, parent(path)+"has_"+leaf(path), value != "")
			value = ""
		}
		if mask && strings.Contains(leaf(path), "url") {
			if text, ok := value.(string); ok {
				value = publicURL(text)
			}
		}
		set(result, path, clone(value))
	}
	s.mu.RLock()
	if s.publicBaseURL != "" {
		result["base_url"] = s.publicBaseURL
	}
	s.mu.RUnlock()
	return result
}
func (s *Service) project(raw map[string]any) map[string]any {
	fields := map[string]any{}
	for path, spec := range specs {
		source := "default"
		if _, ok := get(raw, path); ok {
			source = "configured"
		}
		readOnly := spec.ReadOnly
		s.mu.RLock()
		if path == "base_url" && s.publicBaseURL != "" {
			source = "yaml"
			readOnly = true
		}
		s.mu.RUnlock()
		fields[path] = map[string]any{"source": source, "default": clone(spec.Default), "min": spec.Min, "max": spec.Max, "options": spec.Options, "unit": spec.Unit, "read_only": readOnly, "restart_required": spec.RestartRequired, "sensitive": spec.Sensitive}
	}
	s.mu.RLock()
	startup := clone(s.startup)
	execution := s.execution
	s.mu.RUnlock()
	return map[string]any{"schema_version": 1, "generated_at": time.Now().UTC().Format(time.RFC3339Nano), "revision": revision(raw), "settings": s.values(raw, true), "fields": fields, "startup": startup, "capabilities": map[string]any{"backup_execution": execution.Backup, "webdav_execution": execution.WebDAV, "retention_cleanup_execution": execution.RetentionCleanup, "background_account_checks": execution.BackgroundAccountChecks, "unavailable_label": "部分后台执行能力尚未启用"}}
}

func (s *Service) Patch(ctx context.Context, input map[string]any) (map[string]any, error) {
	expected, ok := input["revision"].(string)
	if !ok || strings.TrimSpace(expected) == "" || len(expected) > 64 {
		return nil, ErrInput
	}
	request := clone(input).(map[string]any)
	delete(request, "revision")
	patch, err := normalizePatch(request, "")
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	lockedBase := s.publicBaseURL != ""
	s.mu.RUnlock()
	if _, ok := patch["base_url"]; ok && lockedBase {
		return nil, ErrInput
	}
	if s.db == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('system_settings'))"); err != nil {
		return nil, ErrStorage
	}
	raw := map[string]any{}
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM system_settings WHERE id=1 FOR UPDATE").Scan(&data)
	if err == nil {
		raw, err = decode(data)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	if revision(raw) != strings.TrimSpace(expected) {
		return nil, ErrConflict
	}
	before := clone(raw).(map[string]any)
	preserveURLCredentials(patch, raw, "")
	clear := false
	if g, ok := patch["genbox_push"].(map[string]any); ok {
		clear, _ = g["clear_push_key"].(bool)
		delete(g, "clear_push_key")
	}
	merge(raw, patch)
	if clear {
		if g, ok := patch["genbox_push"].(map[string]any); !ok || g["push_key"] == nil {
			set(raw, "genbox_push.push_key", "")
		}
	}
	if !reflect.DeepEqual(before, raw) {
		data, err = json.Marshal(raw)
		if err != nil {
			return nil, ErrInput
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO system_settings(id,data,updated_at) VALUES(1,$1,now()) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", data); err != nil {
			return nil, ErrStorage
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrStorage
	}
	changed := []string{}
	for path := range specs {
		a, _ := get(before, path)
		b, _ := get(raw, path)
		if !reflect.DeepEqual(a, b) {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	view := s.project(raw)
	view["changed_fields"] = changed
	view["restart_required"] = false
	return view, nil
}
func normalizePatch(input map[string]any, prefix string) (map[string]any, error) {
	result := map[string]any{}
	for key, value := range input {
		path := prefix + key
		if path == "genbox_push.clear_push_key" {
			if _, ok := value.(bool); !ok {
				return nil, ErrInput
			}
			result[key] = value
			continue
		}
		spec, exists := specs[path]
		if exists {
			if _, numeric := spec.Default.(float64); numeric {
				switch number := value.(type) {
				case int:
					value = float64(number)
				case json.Number:
					n, err := number.Float64()
					if err != nil {
						return nil, ErrInput
					}
					value = n
				}
			}
			if spec.ReadOnly || !valid(value, spec) {
				return nil, ErrInput
			}
			if text, ok := value.(string); ok {
				value = strings.TrimSpace(text)
				if path == "base_url" {
					var err error
					value, err = normalizePublicBaseURL(value.(string))
					if err != nil {
						return nil, ErrInput
					}
				}
				if spec.Sensitive && value == "" {
					continue
				}
			}
			result[key] = value
			continue
		}
		nested, ok := value.(map[string]any)
		if !ok {
			return nil, ErrInput
		}
		found := false
		for candidate := range specs {
			if strings.HasPrefix(candidate, path+".") {
				found = true
				break
			}
		}
		if !found {
			return nil, ErrInput
		}
		part, err := normalizePatch(nested, path+".")
		if err != nil {
			return nil, err
		}
		if len(part) > 0 {
			result[key] = part
		}
	}
	return result, nil
}
func valid(v any, spec fieldSpec) bool {
	if v == nil {
		return false
	}
	switch spec.Default.(type) {
	case bool:
		_, ok := v.(bool)
		return ok
	case float64:
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || (spec.Min != nil && n < *spec.Min) || (spec.Max != nil && n > *spec.Max) {
			return false
		}
		return spec.Kind != "integer" || math.Trunc(n) == n
	case string:
		value, ok := v.(string)
		if !ok {
			return false
		}
		if len(spec.Options) > 0 {
			for _, option := range spec.Options {
				if value == option {
					return true
				}
			}
			return false
		}
		return len(value) <= 1<<20
	case []any:
		items, ok := v.([]any)
		if !ok || len(items) > 10000 {
			return false
		}
		for _, item := range items {
			value, ok := item.(string)
			if !ok {
				return false
			}
			if len(spec.Options) > 0 {
				found := false
				for _, option := range spec.Options {
					if value == option {
						found = true
					}
				}
				if !found {
					return false
				}
			}
		}
		return true
	}
	return false
}

func normalizeStored(value any, spec fieldSpec) any {
	if value == nil {
		return clone(spec.Default)
	}
	switch fallback := spec.Default.(type) {
	case float64:
		n, ok := value.(float64)
		if !ok {
			if numeric, ok := value.(json.Number); ok {
				var err error
				n, err = numeric.Float64()
				if err != nil {
					n = fallback
				}
			} else if text, ok := value.(string); ok {
				var err error
				n, err = strconv.ParseFloat(text, 64)
				if err != nil {
					n = fallback
				}
			} else {
				n = fallback
			}
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			n = fallback
		}
		if spec.Kind == "integer" {
			n = math.Trunc(n)
		}
		if spec.Min != nil && n < *spec.Min {
			n = *spec.Min
		}
		if spec.Max != nil && n > *spec.Max {
			n = *spec.Max
		}
		return n
	case bool:
		if b, ok := value.(bool); ok {
			return b
		}
		if text, ok := value.(string); ok {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "true", "1", "yes", "on":
				return true
			case "false", "0", "no", "off":
				return false
			}
		}
		return fallback
	case []any:
		items, ok := value.([]any)
		if !ok {
			return clone(fallback)
		}
		out := []any{}
		seen := map[string]bool{}
		for _, item := range items {
			str, ok := item.(string)
			if !ok {
				continue
			}
			str = strings.TrimSpace(str)
			if str == "" || seen[str] {
				continue
			}
			allowed := len(spec.Options) == 0
			for _, option := range spec.Options {
				if str == option {
					allowed = true
				}
			}
			if allowed {
				out = append(out, str)
				seen[str] = true
			}
		}
		return out
	default:
		if valid(value, spec) {
			return value
		}
		return clone(spec.Default)
	}
}
func revision(raw map[string]any) string {
	data, _ := json.Marshal(raw)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
func clone(v any) any {
	switch value := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = clone(item)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = clone(item)
		}
		return result
	default:
		return v
	}
}
func get(raw map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var v any = raw
	for _, part := range parts {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func set(raw map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		m, ok := raw[part].(map[string]any)
		if !ok {
			m = map[string]any{}
			raw[part] = m
		}
		raw = m
	}
	raw[parts[len(parts)-1]] = v
}
func parent(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i+1]
	}
	return ""
}
func leaf(path string) string { parts := strings.Split(path, "."); return parts[len(parts)-1] }
func merge(dst, src map[string]any) {
	for key, value := range src {
		if part, ok := value.(map[string]any); ok {
			base, ok := dst[key].(map[string]any)
			if !ok {
				base = map[string]any{}
				dst[key] = base
			}
			merge(base, part)
		} else {
			dst[key] = clone(value)
		}
	}
}
func publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}
func normalizePublicBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	u, err := managedhttp.ParseURL(raw)
	if err != nil || strings.Contains(raw, "#") {
		return "", ErrInput
	}
	return u.String(), nil
}
func preserveURLCredentials(patch, raw map[string]any, prefix string) {
	for key, value := range patch {
		path := prefix + key
		if part, ok := value.(map[string]any); ok {
			preserveURLCredentials(part, raw, path+".")
			continue
		}
		if strings.Contains(key, "url") {
			text, ok := value.(string)
			old, _ := get(raw, path)
			oldText, _ := old.(string)
			if ok && oldText != "" && text == publicURL(oldText) {
				patch[key] = oldText
			}
		}
	}
}
