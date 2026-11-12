package remoteimports

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/config"
)

type Options struct {
	DB           *sql.DB
	Accounts     *accounts.Repository
	Upstream     config.Upstream
	ResolveProxy func(context.Context) (string, error)
}
type Service struct {
	db       *sql.DB
	accounts *accounts.Repository
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closing  bool
	wg       sync.WaitGroup
	oauth    *oauthFlow
}

func New(parent context.Context, options Options) (*Service, error) {
	if options.DB == nil || options.Accounts == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{db: options.DB, accounts: options.Accounts, ctx: ctx, cancel: cancel, oauth: newOAuthFlow(options)}
	err := s.update(parent, func(all map[string][]map[string]any) error {
		for _, items := range all {
			for _, item := range items {
				if active(item["import_job"]) {
					j := readJob(item["import_job"])
					j.Status = "failed"
					j.Error = "服务已重启，导入已中断；已保存账号保留"
					j.UpdatedAt = now()
					j.project()
					item["import_job"] = jobMap(j)
				}
			}
		}
		return nil
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return s, nil
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closing = true
	s.cancel()
	s.oauth.close()
	s.mu.Unlock()
	s.wg.Wait()
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID() (string, error) {
	var raw [16]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(raw[:]), nil
}
func text(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}
func integer(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}
func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if raw, ok := v.(string); ok && len(raw) <= maxRemoteBytes {
		var out map[string]any
		if json.Unmarshal([]byte(raw), &out) == nil {
			return out
		}
	}
	return nil
}
func providerOK(provider string) bool { return provider == "cpa" || provider == "sub2api" }
func active(value any) bool {
	m := object(value)
	status := text(m["status"])
	return status == "pending" || status == "running" || status == "queued"
}

func (s *Service) load(ctx context.Context, provider string) ([]map[string]any, error) {
	if !providerOK(provider) {
		return nil, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var data []byte
	e := s.db.QueryRowContext(ctx, "SELECT items FROM remote_import_configurations WHERE provider=$1", provider).Scan(&data)
	if errors.Is(e, sql.ErrNoRows) {
		return []map[string]any{}, nil
	}
	if e != nil {
		return nil, ErrStorage
	}
	return decodeItems(data)
}
func decodeItems(data []byte) ([]map[string]any, error) {
	if len(data) > 16<<20 {
		return nil, ErrStorage
	}
	var items []map[string]any
	if json.Unmarshal(data, &items) != nil || items == nil || len(items) > maxSources {
		return nil, ErrStorage
	}
	for _, item := range items {
		if item == nil || text(item["id"]) == "" {
			return nil, ErrStorage
		}
	}
	return items, nil
}
func (s *Service) update(parent context.Context, fn func(map[string][]map[string]any) error) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('remote_import_configuration:global'))"); e != nil {
		return ErrStorage
	}
	rows, e := tx.QueryContext(ctx, "SELECT provider,items FROM remote_import_configurations WHERE provider IN ('cpa','sub2api') ORDER BY provider FOR UPDATE")
	if e != nil {
		return ErrStorage
	}
	all := map[string][]map[string]any{"cpa": {}, "sub2api": {}}
	before := map[string][]byte{}
	for rows.Next() {
		var p string
		var data []byte
		if rows.Scan(&p, &data) != nil {
			rows.Close()
			return ErrStorage
		}
		items, e := decodeItems(data)
		if e != nil {
			rows.Close()
			return e
		}
		all[p] = items
		before[p] = data
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return ErrStorage
	}
	if e = fn(all); e != nil {
		return e
	}
	for _, p := range []string{"cpa", "sub2api"} {
		data, e := json.Marshal(all[p])
		if e != nil || len(data) > 16<<20 {
			return ErrStorage
		}
		var old any
		_ = json.Unmarshal(before[p], &old)
		var next any
		_ = json.Unmarshal(data, &next)
		if reflect.DeepEqual(old, next) || (before[p] == nil && len(all[p]) == 0) {
			continue
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO remote_import_configurations(provider,items,revision,updated_at) VALUES($1,$2,1,now()) ON CONFLICT(provider) DO UPDATE SET items=EXCLUDED.items,revision=remote_import_configurations.revision+1,updated_at=now()`, p, data); e != nil {
			return ErrStorage
		}
	}
	if tx.Commit() != nil {
		return ErrStorage
	}
	return nil
}
func publicSource(provider string, item map[string]any) map[string]any {
	baseURL := ""
	if u, err := managementURL(text(item["base_url"])); err == nil {
		baseURL = u.String()
	}
	out := map[string]any{"id": text(item["id"]), "name": text(item["name"]), "base_url": baseURL, "import_job": readJob(item["import_job"])}
	if provider == "sub2api" {
		out["email"] = text(item["email"])
		out["has_api_key"] = text(item["api_key"]) != ""
		out["group_id"] = text(item["group_id"])
	}
	return out
}
func (s *Service) List(ctx context.Context, provider string) ([]map[string]any, error) {
	items, e := s.load(ctx, provider)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, publicSource(provider, item))
	}
	return out, nil
}
func (s *Service) source(ctx context.Context, provider, id string) (map[string]any, error) {
	items, e := s.load(ctx, provider)
	if e != nil {
		return nil, e
	}
	for _, item := range items {
		if text(item["id"]) == id {
			return item, nil
		}
	}
	return nil, ErrNotFound
}
func (s *Service) Save(ctx context.Context, provider, id string, updates map[string]any) (map[string]any, error) {
	if !providerOK(provider) {
		return nil, ErrInput
	}
	for key, value := range updates {
		v, ok := value.(string)
		if !ok || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			return nil, ErrInput
		}
		switch key {
		case "name", "base_url":
		case "secret_key":
			if provider != "cpa" {
				return nil, ErrInput
			}
		case "email", "password", "api_key", "group_id":
			if provider != "sub2api" {
				return nil, ErrInput
			}
		default:
			return nil, ErrInput
		}
	}
	create := id == ""
	if create {
		var e error
		id, e = newID()
		if e != nil {
			return nil, e
		}
	}
	var result map[string]any
	e := s.update(ctx, func(all map[string][]map[string]any) error {
		items := all[provider]
		var item map[string]any
		for _, candidate := range items {
			if text(candidate["id"]) == id {
				item = candidate
				break
			}
		}
		if item == nil {
			if !create {
				return ErrNotFound
			}
			if len(items) >= maxSources {
				return ErrInput
			}
			item = map[string]any{"id": id}
			items = append(items, item)
			all[provider] = items
		}
		if active(item["import_job"]) {
			return ErrConflict
		}
		for key, value := range updates {
			if (key == "secret_key" || key == "password" || key == "api_key") && text(value) == "" && !create {
				continue
			}
			item[key] = text(value)
		}
		u, e := managementURL(text(item["base_url"]))
		if e != nil {
			return e
		}
		item["base_url"] = u.String()
		if len(text(item["name"])) > 256 || len(text(item["email"])) > 512 || len(text(item["group_id"])) > 128 {
			return ErrInput
		}
		if provider == "cpa" && text(item["secret_key"]) == "" {
			return ErrInput
		}
		if provider == "sub2api" && text(item["api_key"]) == "" && (text(item["email"]) == "" || text(item["password"]) == "") {
			return ErrInput
		}
		result = publicSource(provider, item)
		return nil
	})
	return result, e
}
func (s *Service) Delete(ctx context.Context, provider, id string) error {
	if !providerOK(provider) {
		return ErrInput
	}
	return s.update(ctx, func(all map[string][]map[string]any) error {
		for i, item := range all[provider] {
			if text(item["id"]) == id {
				if active(item["import_job"]) {
					return ErrConflict
				}
				all[provider] = append(all[provider][:i], all[provider][i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}
func (s *Service) Job(ctx context.Context, provider, id string) (*Job, error) {
	item, e := s.source(ctx, provider, id)
	if e != nil {
		return nil, e
	}
	return readJob(item["import_job"]), nil
}
