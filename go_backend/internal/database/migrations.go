package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	goosedb "github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockID int64 = 74002331811215
const baselineFingerprint = "4f2d00c4570d4fc091901b32c86e7c78ef0916b59a070c3d267a172a56629285"

var (
	ErrNeedsMigration    = errors.New("数据库尚未初始化，请执行 db migrate")
	ErrNeedsAdoption     = errors.New("发现受支持的无台账结构，请停止写入并执行 db adopt --offline")
	ErrUnsupportedSchema = errors.New("数据库结构或数据不变量不受支持，未执行接管或修改")
	ErrNewerSchema       = errors.New("数据库版本高于当前程序，拒绝启动或迁移")
)

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Status struct {
	State   string `json:"state"`
	Version int64  `json:"version"`
}

// Status 只读检查，不创建 goose 台账。
func (d *Database) Status(ctx context.Context) (Status, error) {
	tx, err := d.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()
	return schemaStatus(ctx, tx)
}

func (d *Database) Verify(ctx context.Context) error {
	status, err := d.Status(ctx)
	if err != nil {
		return err
	}
	switch status.State {
	case "empty":
		return ErrNeedsMigration
	case "legacy":
		return ErrNeedsAdoption
	case "ready":
		return nil
	default:
		return ErrUnsupportedSchema
	}
}

func schemaStatus(ctx context.Context, q querier) (Status, error) {
	var ledger *string
	if err := q.QueryRowContext(ctx, "SELECT to_regclass('public.goose_db_version')::text").Scan(&ledger); err != nil {
		return Status{}, err
	}
	version := int64(0)
	if ledger != nil {
		var invalid bool
		if err := q.QueryRowContext(ctx, "SELECT COALESCE(max(version_id),0),COALESCE(bool_or(NOT is_applied OR version_id<0),false) FROM public.goose_db_version").Scan(&version, &invalid); err != nil {
			return Status{}, ErrUnsupportedSchema
		}
		if version > 1 {
			return Status{}, ErrNewerSchema
		}
		if invalid {
			return Status{}, ErrUnsupportedSchema
		}
	}
	fingerprint, count, err := catalogFingerprint(ctx, q)
	if err != nil {
		return Status{}, err
	}
	if count == 0 && fingerprint == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" && version == 0 {
		return Status{State: "empty"}, nil
	}
	if fingerprint != baselineFingerprint {
		return Status{}, fmt.Errorf("%w（结构指纹 %s）", ErrUnsupportedSchema, fingerprint)
	}
	if err := verifyData(ctx, q); err != nil {
		return Status{}, err
	}
	if version == 0 {
		if ledger != nil {
			return Status{}, ErrUnsupportedSchema
		}
		return Status{State: "legacy"}, nil
	}
	return Status{State: "ready", Version: version}, nil
}

// catalogFingerprint 覆盖列类型/默认值/空值、主外键、检查约束、索引和策略。
// 排除对象所有者、OID、序列当前值和 goose 台账，因此接管不会修改业务记录。
func catalogFingerprint(ctx context.Context, q querier) (string, int, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind,object,definition FROM (
	SELECT 'table'::text kind,c.relname::text object,c.relkind::text||':'||c.relrowsecurity::text||':'||c.relforcerowsecurity::text definition
	FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname<>'goose_db_version'
	UNION ALL
	SELECT 'column',c.relname||'.'||a.attname,format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text||':'||COALESCE(pg_get_expr(ad.adbin,ad.adrelid),'')||':'||a.attidentity::text||':'||a.attgenerated::text
	FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attrdef ad ON ad.adrelid=c.oid AND ad.adnum=a.attnum
	WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname<>'goose_db_version' AND a.attnum>0 AND NOT a.attisdropped
	UNION ALL
	SELECT 'constraint',c.relname||'.'||co.conname,pg_get_constraintdef(co.oid,true)
	FROM pg_constraint co JOIN pg_class c ON c.oid=co.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname<>'goose_db_version'
	UNION ALL
	SELECT 'index',i.relname,pg_get_indexdef(i.oid)||':'||ix.indisvalid::text||':'||ix.indisready::text
	FROM pg_index ix JOIN pg_class i ON i.oid=ix.indexrelid JOIN pg_class c ON c.oid=ix.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname<>'goose_db_version'
	UNION ALL
	SELECT 'trigger',c.relname||'.'||t.tgname,pg_get_triggerdef(t.oid)||':'||t.tgenabled::text
	FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT t.tgisinternal AND c.relname<>'goose_db_version'
	UNION ALL
	SELECT 'view',c.relname,pg_get_viewdef(c.oid,true) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('v','m')
	UNION ALL
	SELECT 'policy',c.relname||'.'||p.polname,p.polcmd::text||':'||p.polpermissive::text||':'||COALESCE(pg_get_expr(p.polqual,p.polrelid),'')||':'||COALESCE(pg_get_expr(p.polwithcheck,p.polrelid),'')
	FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'
	UNION ALL
	SELECT 'sequence',c.relname,format_type(s.seqtypid,NULL)||':'||s.seqstart::text||':'||s.seqincrement::text||':'||s.seqmax::text||':'||s.seqmin::text||':'||s.seqcache::text||':'||s.seqcycle::text||':'||COALESCE(t.relname||'.'||a.attname,'')
	FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid JOIN pg_namespace n ON n.oid=c.relnamespace
	LEFT JOIN pg_depend d ON d.objid=c.oid AND d.classid='pg_class'::regclass AND d.deptype IN ('a','i')
	LEFT JOIN pg_class t ON t.oid=d.refobjid LEFT JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid
	WHERE n.nspname='public' AND COALESCE(t.relname,'')<>'goose_db_version'
	) objects ORDER BY kind,object,definition`)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	encoder := json.NewEncoder(hash)
	for rows.Next() {
		var kind, object, definition string
		if err := rows.Scan(&kind, &object, &definition); err != nil {
			return "", 0, err
		}
		if kind == "table" {
			count++
		}
		if err := encoder.Encode([]string{kind, object, definition}); err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), count, rows.Err()
}

func verifyData(ctx context.Context, q querier) error {
	var invalid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage_revisions WHERE version<0)
	OR (SELECT count(*) FROM storage_revisions WHERE collection IN ('accounts','auth_keys'))<>2
	OR EXISTS(SELECT 1 FROM accounts WHERE jsonb_typeof(data::jsonb)<>'object')
	OR EXISTS(SELECT 1 FROM auth_keys WHERE jsonb_typeof(data::jsonb)<>'object')`).Scan(&invalid)
	if err != nil || invalid {
		return ErrUnsupportedSchema
	}
	rows, err := q.QueryContext(ctx, `SELECT c.relname,a.attname,pg_get_serial_sequence(format('%I.%I',n.nspname,c.relname),a.attname)
	FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
	WHERE n.nspname='public' AND c.relkind='r' AND c.relname IN ('accounts','auth_keys','call_records') AND a.attnum>0 AND NOT a.attisdropped
	AND pg_get_serial_sequence(format('%I.%I',n.nspname,c.relname),a.attname) IS NOT NULL`)
	if err != nil {
		return ErrUnsupportedSchema
	}
	type sequenceColumn struct{ table, column, sequence string }
	var sequences []sequenceColumn
	for rows.Next() {
		var item sequenceColumn
		if err := rows.Scan(&item.table, &item.column, &item.sequence); err != nil {
			rows.Close()
			return ErrUnsupportedSchema
		}
		sequences = append(sequences, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return ErrUnsupportedSchema
	}
	for _, item := range sequences {
		var maximum sql.NullInt64
		if err := q.QueryRowContext(ctx, "SELECT max("+pgx.Identifier{item.column}.Sanitize()+") FROM "+pgx.Identifier{"public", item.table}.Sanitize()).Scan(&maximum); err != nil {
			return ErrUnsupportedSchema
		}
		var last int64
		var called bool
		if err := q.QueryRowContext(ctx, "SELECT last_value,is_called FROM "+item.sequence).Scan(&last, &called); err != nil {
			return ErrUnsupportedSchema
		}
		if maximum.Valid && (last < maximum.Int64 || (last == maximum.Int64 && !called)) {
			return ErrUnsupportedSchema
		}
	}
	return nil
}

func (d *Database) Migrate(ctx context.Context) error {
	status, err := d.Status(ctx)
	if err != nil {
		return err
	}
	if status.State == "legacy" {
		return ErrNeedsAdoption
	}
	if status.State == "ready" {
		return nil
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, d.SQL, files, goose.WithSessionLocker(&schemaLocker{SessionLocker: locker}))
	if err != nil {
		return err
	}
	// Up 会先调用不加锁且可能建台账的 HasPending。显式基线使用先锁定的 ApplyVersion。
	if _, err := provider.ApplyVersion(ctx, 1, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
		return errors.New("PostgreSQL 迁移失败，请检查结构、锁和权限；事务变更已回滚")
	}
	return d.Verify(ctx)
}

type schemaLocker struct{ lock.SessionLocker }

func (l *schemaLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	if err := l.SessionLocker.SessionLock(ctx, conn); err != nil {
		return err
	}
	status, err := schemaStatus(ctx, conn)
	if err == nil && status.State == "legacy" {
		err = ErrNeedsAdoption
	}
	if err != nil {
		l.SessionUnlock(context.Background(), conn)
		return err
	}
	return nil
}

func (l *schemaLocker) SessionUnlock(_ context.Context, conn *sql.Conn) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := l.SessionLocker.SessionUnlock(cleanup, conn)
	if err != nil {
		conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err
}

// Adopt 仅由已明确传入 --offline 的 CLI 调用；在同一事务锁住完整结构并登记。
func (d *Database) Adopt(ctx context.Context) error {
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID))
	if err != nil {
		return err
	}
	if err := locker.SessionLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := locker.SessionUnlock(cleanup, conn); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status, err := schemaStatus(ctx, tx)
	if err != nil {
		return err
	}
	if status.State == "ready" {
		return nil
	}
	if status.State != "legacy" {
		return ErrUnsupportedSchema
	}
	rows, err := tx.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, pgx.Identifier{"public", name}.Sanitize())
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+strings.Join(tables, ",")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	if _, err := schemaStatus(ctx, tx); err != nil {
		return err
	}
	store, err := goosedb.NewStore(goosedb.DialectPostgres, "goose_db_version")
	if err != nil {
		return err
	}
	if err := store.CreateVersionTable(ctx, tx); err != nil {
		return err
	}
	for _, version := range []int64{0, 1} {
		if err := store.Insert(ctx, tx, goosedb.InsertRequest{Version: version}); err != nil {
			return err
		}
	}
	if _, err := schemaStatus(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
