// Package database 组装唯一连接池；启动与显式迁移共用受控版本迁移。
package database

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/ent"
	"chatgpt2api/go_backend/internal/config"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

var (
	ErrConnection  = errors.New("无法连接 PostgreSQL，请检查 DB_* 配置、网络和数据库权限")
	ErrEnvironment = errors.New("请移除 PG* 连接环境变量，数据库配置使用 .env 或进程环境的 DB_* 变量")
)

type Database struct {
	SQL      *sql.DB
	Ent      *ent.Client
	once     sync.Once
	closeErr error
}

// Open 的 createMissing 由命令语义选择；status/verify 不创建数据库。
func Open(ctx context.Context, cfg config.Database, createMissing bool) (*Database, error) {
	if cfg.Type != "postgresql" {
		return nil, errors.New("Go 后端只支持 PostgreSQL")
	}
	connection, err := connectionConfig(cfg)
	if err != nil {
		if errors.Is(err, ErrEnvironment) {
			return nil, err
		}
		return nil, ErrConnection
	}
	db := stdlib.OpenDB(*connection)
	db.SetMaxOpenConns(cfg.MaxOpenConnections)
	db.SetMaxIdleConns(cfg.MaxIdleConnections)
	db.SetConnMaxLifetime(time.Duration(cfg.ConnectionMaxLifetimeSeconds) * time.Second)
	if err := db.PingContext(ctx); err != nil {
		var pgerr *pgconn.PgError
		if !createMissing || !errors.As(err, &pgerr) || pgerr.Code != "3D000" {
			db.Close()
			return nil, ErrConnection
		}
		if err := createDatabase(ctx, cfg); err != nil {
			db.Close()
			return nil, err
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, ErrConnection
		}
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	return &Database{SQL: db, Ent: client}, nil
}

func connectionConfig(cfg config.Database) (*pgx.ConnConfig, error) {
	// pgx 沿用 libpq 的环境规则；拒绝歧义，且不临时改写进程环境。
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			return nil, ErrEnvironment
		}
	}
	u := &url.URL{Scheme: "postgresql", Host: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), User: url.UserPassword(cfg.Username, cfg.Password), Path: "/" + cfg.Name}
	u.RawPath = "/" + url.PathEscape(cfg.Name)
	u.RawQuery = url.Values{"sslmode": {cfg.SSLMode}, "connect_timeout": {strconv.Itoa(cfg.ConnectTimeoutSeconds)}, "passfile": {""}, "sslcert": {""}, "sslkey": {""}, "sslrootcert": {""}}.Encode()
	parsed, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	// 不继承 PGOPTIONS、PGSERVICE 等环境变量的隐式会话设置。
	parsed.RuntimeParams = map[string]string{"application_name": "chatgpt2api-go", "search_path": "public", "statement_timeout": strconv.Itoa(cfg.OperationTimeoutSeconds * 1000)}
	return parsed, nil
}

func createDatabase(ctx context.Context, cfg config.Database) error {
	maintenance := cfg
	maintenance.Name = "postgres"
	if cfg.Name == "postgres" {
		maintenance.Name = "template1"
	}
	parsed, err := connectionConfig(maintenance)
	if err != nil {
		return ErrConnection
	}
	conn, err := pgx.ConnectConfig(ctx, parsed)
	if err != nil {
		return ErrConnection
	}
	defer conn.Close(context.Background())
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", cfg.Name).Scan(&exists); err != nil {
		return ErrConnection
	}
	if exists {
		return nil
	}
	_, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cfg.Name}.Sanitize())
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && (pgerr.Code == "42P04" || pgerr.Code == "23505") {
			return nil
		}
		return errors.New("已确认目标库不存在，但创建失败；请检查维护库和 CREATEDB 权限")
	}
	return nil
}

// Close 由应用入口统一调用；Ent 关闭其包装的同一个 sql.DB。
func (d *Database) Close() error {
	d.once.Do(func() { d.closeErr = d.Ent.Close() })
	return d.closeErr
}
