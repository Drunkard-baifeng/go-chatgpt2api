package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// NewPostgres 保持每次查库鉴权，仅对成功使用的时间做进程内合并和限频写入。
func NewPostgres(adminKey string, db *sql.DB, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	s := New(adminKey, func(ctx context.Context, digest string) (*Key, error) {
		operation, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return LookupPostgres(operation, db, digest)
	})
	s.usage = &keyUsage{db: db, timeout: timeout, now: time.Now, items: make(map[string]*keyUse)}
	return s
}

// LookupPostgres 读取现有 TEXT JSON，不改写记录或维护另一份身份缓存。
func LookupPostgres(ctx context.Context, db *sql.DB, digest string) (*Key, error) {
	rows, err := db.QueryContext(ctx, `SELECT data FROM auth_keys WHERE btrim(data::jsonb->>'key_hash')=$1 ORDER BY id`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var item struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Role    string          `json:"role"`
			KeyHash string          `json:"key_hash"`
			Enabled json.RawMessage `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			return nil, errors.New("用户密钥记录无效")
		}
		enabled := true
		if len(item.Enabled) > 0 {
			enabled = false
			if string(item.Enabled) != "null" {
				if err := json.Unmarshal(item.Enabled, &enabled); err != nil {
					return nil, errors.New("用户密钥状态无效")
				}
			}
		}
		role := strings.ToLower(strings.TrimSpace(item.Role))
		id := strings.TrimSpace(item.ID)
		if id == "" || (role != "admin" && role != "user") {
			continue
		}
		return &Key{ID: id, Name: strings.TrimSpace(item.Name), Role: role, KeyHash: strings.TrimSpace(item.KeyHash), Enabled: enabled}, nil
	}
	return nil, rows.Err()
}

type keyUse struct {
	at, attempted time.Time
	writing       bool
}

// keyUsage 不缓存身份或密钥，只拥有最近使用时间；数据库 IO 不持有内存锁。
type keyUsage struct {
	mu      sync.Mutex
	items   map[string]*keyUse
	db      *sql.DB
	timeout time.Duration
	now     func() time.Time
}

func (u *keyUsage) record(parent context.Context, key *Key) {
	if u == nil {
		return
	}
	now := u.now().UTC()
	u.mu.Lock()
	item := u.items[key.ID]
	if item == nil {
		item = &keyUse{}
		u.items[key.ID] = item
	}
	if now.After(item.at) {
		item.at = now
	}
	flush := !item.writing && (item.attempted.IsZero() || now.Sub(item.attempted) >= time.Minute)
	if flush {
		item.attempted, item.writing = now, true
	}
	at := item.at
	u.mu.Unlock()
	if !flush {
		return
	}
	// 记录失败不撤销已经成功的鉴权；当前进程仍能投影最新时间。
	ctx, cancel := context.WithTimeout(parent, u.timeout)
	defer cancel()
	_ = u.persist(ctx, key, at)
	u.mu.Lock()
	item.writing = false
	u.mu.Unlock()
}

func (u *keyUsage) latest(id string) time.Time {
	if u == nil {
		return time.Time{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if item := u.items[id]; item != nil {
		return item.at
	}
	return time.Time{}
}

func (u *keyUsage) forget(id string) {
	if u != nil {
		u.mu.Lock()
		delete(u.items, id)
		u.mu.Unlock()
	}
}

func (u *keyUsage) persist(ctx context.Context, key *Key, at time.Time) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 与管理 CRUD 使用相同修订锁，避免整条 JSON 的管理更新覆盖使用时间。
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM storage_revisions WHERE collection='auth_keys' FOR UPDATE`).Scan(&version); err != nil {
		return err
	}
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT data::jsonb->>'last_used_at' FROM auth_keys WHERE key_id=$1`, key.ID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if stored, err := time.Parse(time.RFC3339Nano, previous.String); err == nil && !at.After(stored) {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE auth_keys
		SET data=jsonb_set(data::jsonb,'{last_used_at}',to_jsonb($1::text))::text
		WHERE key_id=$2 AND btrim(data::jsonb->>'key_hash')=$3
		AND btrim(data::jsonb->>'id')=$2
		AND lower(btrim(data::jsonb->>'role')) IN ('admin','user')
		AND COALESCE(data::jsonb->'enabled','true'::jsonb)='true'::jsonb`, at.Format(time.RFC3339Nano), key.ID, key.KeyHash)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE storage_revisions SET version=version+1 WHERE collection='auth_keys'`); err != nil {
		return err
	}
	return tx.Commit()
}
