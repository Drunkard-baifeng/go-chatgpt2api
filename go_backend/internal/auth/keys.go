package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrKeyStorage  = errors.New("用户密钥仓储暂不可用")
	ErrKeyInput    = errors.New("用户密钥输入无效")
	ErrKeyConflict = errors.New("用户密钥或名称已经存在，请使用新的值")
	ErrKeyNotFound = errors.New("这条用户密钥不存在，可能已经被删除")
)

// Keys 拥有用户密钥的写入与安全投影；鉴权仍直接读取同一个 PostgreSQL 表。
type Keys struct {
	db       *sql.DB
	timeout  time.Duration
	identity *Service
}

func NewKeys(db *sql.DB, timeout time.Duration, identity *Service) *Keys {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Keys{db: db, timeout: timeout, identity: identity}
}

type KeyView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Role       string  `json:"role"`
	Enabled    bool    `json:"enabled"`
	CreatedAt  *string `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

type KeyCreate struct {
	Name string `json:"name"`
}

type KeyUpdate struct {
	Name    *string `json:"name"`
	Enabled *bool   `json:"enabled"`
	Key     *string `json:"key"`
}

type keyRecord struct {
	id   string
	data map[string]json.RawMessage
}

func keyString(data map[string]json.RawMessage, field string) string {
	var value string
	_ = json.Unmarshal(data[field], &value)
	return strings.TrimSpace(value)
}

func keySet(data map[string]json.RawMessage, field string, value any) {
	data[field], _ = json.Marshal(value)
}

func (s *Keys) projection(record keyRecord) (KeyView, error) {
	enabled := true
	if raw, found := record.data["enabled"]; found {
		enabled = false
		if string(raw) != "null" && json.Unmarshal(raw, &enabled) != nil {
			return KeyView{}, ErrKeyStorage
		}
	}
	name := keyString(record.data, "name")
	if name == "" {
		name = "普通用户"
	}
	view := KeyView{ID: record.id, Name: name, Role: "user", Enabled: enabled}
	for field, target := range map[string]**string{"created_at": &view.CreatedAt, "last_used_at": &view.LastUsedAt} {
		if value := keyString(record.data, field); value != "" {
			*target = &value
		}
	}
	if s.identity != nil {
		latest := s.identity.usage.latest(record.id)
		var stored time.Time
		if view.LastUsedAt != nil {
			stored, _ = time.Parse(time.RFC3339Nano, *view.LastUsedAt)
		}
		if latest.After(stored) {
			value := latest.Format(time.RFC3339Nano)
			view.LastUsedAt = &value
		}
	}
	return view, nil
}

func keyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrKeyInput
	}
	return name, nil
}

func (s *Keys) keyDigest(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 65536 || !utf8.ValidString(raw) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", ErrKeyInput
	}
	digest := sha256.Sum256([]byte(raw))
	if s.identity != nil && s.identity.hasAdmin && subtle.ConstantTimeCompare(digest[:], s.identity.adminHash[:]) == 1 {
		return "", ErrKeyConflict
	}
	return hex.EncodeToString(digest[:]), nil
}

type keyQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadKeys(ctx context.Context, query keyQuerier) ([]keyRecord, error) {
	rows, err := query.QueryContext(ctx, `SELECT key_id,data FROM auth_keys ORDER BY id`)
	if err != nil {
		return nil, ErrKeyStorage
	}
	defer rows.Close()
	items := []keyRecord{}
	for rows.Next() {
		var id, encoded string
		if rows.Scan(&id, &encoded) != nil {
			return nil, ErrKeyStorage
		}
		var data map[string]json.RawMessage
		if json.Unmarshal([]byte(encoded), &data) != nil || data == nil || id != keyString(data, "id") {
			return nil, ErrKeyStorage
		}
		items = append(items, keyRecord{id: id, data: data})
	}
	if rows.Err() != nil {
		return nil, ErrKeyStorage
	}
	return items, nil
}

func (s *Keys) List(parent context.Context) ([]KeyView, error) {
	if s == nil || s.db == nil {
		return nil, ErrKeyStorage
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	items, err := loadKeys(ctx, s.db)
	if err != nil {
		return nil, keyFailure(ctx, err)
	}
	result := []KeyView{}
	for _, item := range items {
		if keyString(item.data, "role") != "user" || keyString(item.data, "key_hash") == "" {
			continue
		}
		view, err := s.projection(item)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func keyFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// mutate 用同一集合修订行锁串行化名称和哈希唯一性校验，避免并发创建或轮换覆盖。
func (s *Keys) mutate(parent context.Context, fn func(context.Context, *sql.Tx, []keyRecord) (bool, error)) error {
	if s == nil || s.db == nil {
		return ErrKeyStorage
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return keyFailure(ctx, ErrKeyStorage)
	}
	defer tx.Rollback()
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM storage_revisions WHERE collection='auth_keys' FOR UPDATE`).Scan(&version); err != nil {
		return keyFailure(ctx, ErrKeyStorage)
	}
	items, err := loadKeys(ctx, tx)
	if err != nil {
		return keyFailure(ctx, err)
	}
	changed, err := fn(ctx, tx, items)
	if err != nil {
		return keyFailure(ctx, err)
	}
	if changed {
		if _, err = tx.ExecContext(ctx, `UPDATE storage_revisions SET version=version+1 WHERE collection='auth_keys'`); err != nil {
			return keyFailure(ctx, ErrKeyStorage)
		}
	}
	if err = tx.Commit(); err != nil {
		return keyFailure(ctx, ErrKeyStorage)
	}
	return nil
}

func uniqueKeyName(items []keyRecord, name, exclude string) (string, error) {
	used := func(candidate string) bool {
		for _, item := range items {
			if item.id != exclude && keyString(item.data, "role") == "user" && keyString(item.data, "name") == candidate {
				return true
			}
		}
		return false
	}
	if name != "" {
		if used(name) {
			return "", ErrKeyConflict
		}
		return name, nil
	}
	name = "普通用户"
	for suffix := 2; used(name); suffix++ {
		name = "普通用户 " + strconv.Itoa(suffix)
	}
	return name, nil
}

func uniqueKeyDigest(items []keyRecord, digest, exclude string) error {
	for _, item := range items {
		if item.id != exclude && subtle.ConstantTimeCompare([]byte(keyString(item.data, "key_hash")), []byte(digest)) == 1 {
			return ErrKeyConflict
		}
	}
	return nil
}

func (s *Keys) Create(parent context.Context, request KeyCreate) (KeyView, string, error) {
	name, err := keyName(request.Name)
	if err != nil {
		return KeyView{}, "", err
	}
	var view KeyView
	var raw string
	err = s.mutate(parent, func(ctx context.Context, tx *sql.Tx, items []keyRecord) (bool, error) {
		name, err = uniqueKeyName(items, name, "")
		if err != nil {
			return false, err
		}
		var id, digest string
		for attempt := 0; attempt < 4; attempt++ {
			var bytes [30]byte
			if _, err = rand.Read(bytes[:]); err != nil {
				return false, ErrKeyStorage
			}
			id = hex.EncodeToString(bytes[:6])
			raw = "sk-" + base64.RawURLEncoding.EncodeToString(bytes[6:])
			digest, err = s.keyDigest(raw)
			if err == nil {
				err = uniqueKeyDigest(items, digest, "")
			}
			for _, item := range items {
				if item.id == id {
					err = ErrKeyConflict
				}
			}
			if err == nil {
				break
			}
		}
		if err != nil {
			return false, ErrKeyStorage
		}
		data := map[string]json.RawMessage{}
		for field, value := range map[string]any{"id": id, "name": name, "role": "user", "enabled": true, "key_hash": digest, "created_at": time.Now().UTC().Format(time.RFC3339), "last_used_at": nil} {
			keySet(data, field, value)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return false, ErrKeyStorage
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO auth_keys(key_id,data) VALUES($1,$2)`, id, string(encoded)); err != nil {
			return false, ErrKeyStorage
		}
		view, err = s.projection(keyRecord{id: id, data: data})
		return true, err
	})
	if err != nil {
		return KeyView{}, "", err
	}
	return view, raw, nil
}

func validKeyID(id string) bool {
	return id != "" && len(id) <= 255 && utf8.ValidString(id) && strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == '/' || r == '\\' }) < 0
}

func (s *Keys) Update(parent context.Context, id string, request KeyUpdate) (KeyView, error) {
	if !validKeyID(id) {
		return KeyView{}, ErrKeyNotFound
	}
	if request.Name == nil && request.Enabled == nil && request.Key == nil {
		return KeyView{}, ErrKeyInput
	}
	var name, digest string
	var err error
	if request.Name != nil {
		name, err = keyName(*request.Name)
		if err != nil {
			return KeyView{}, err
		}
	}
	if request.Key != nil {
		digest, err = s.keyDigest(*request.Key)
		if err != nil {
			return KeyView{}, err
		}
	}
	var view KeyView
	err = s.mutate(parent, func(ctx context.Context, tx *sql.Tx, items []keyRecord) (bool, error) {
		for _, item := range items {
			if item.id != id || keyString(item.data, "role") != "user" {
				continue
			}
			before := make(map[string]json.RawMessage, len(item.data))
			for k, v := range item.data {
				before[k] = v
			}
			if request.Name != nil {
				name, err = uniqueKeyName(items, name, id)
				if err != nil {
					return false, err
				}
				keySet(item.data, "name", name)
			}
			if request.Key != nil {
				if err = uniqueKeyDigest(items, digest, id); err != nil {
					return false, err
				}
				keySet(item.data, "key_hash", digest)
			}
			if request.Enabled != nil {
				keySet(item.data, "enabled", *request.Enabled)
			}
			view, err = s.projection(item)
			if err != nil {
				return false, err
			}
			if reflect.DeepEqual(before, item.data) {
				return false, nil
			}
			encoded, err := json.Marshal(item.data)
			if err != nil {
				return false, ErrKeyStorage
			}
			if _, err = tx.ExecContext(ctx, `UPDATE auth_keys SET data=$1 WHERE key_id=$2`, string(encoded), id); err != nil {
				return false, ErrKeyStorage
			}
			return true, nil
		}
		return false, ErrKeyNotFound
	})
	return view, err
}

func (s *Keys) Delete(parent context.Context, id string) error {
	if !validKeyID(id) {
		return ErrKeyNotFound
	}
	err := s.mutate(parent, func(ctx context.Context, tx *sql.Tx, items []keyRecord) (bool, error) {
		for _, item := range items {
			if item.id == id && keyString(item.data, "role") == "user" {
				if _, err := tx.ExecContext(ctx, `DELETE FROM auth_keys WHERE key_id=$1`, id); err != nil {
					return false, ErrKeyStorage
				}
				return true, nil
			}
		}
		return false, ErrKeyNotFound
	})
	if err == nil && s.identity != nil {
		s.identity.usage.forget(id)
	}
	return err
}
