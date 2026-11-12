package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type AddFile = func(name string, size int64, reader io.Reader) error
type Snapshot func(context.Context, AddFile) error
type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, ErrLimit
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

// dumpPostgres只使用显式连接参数，移除全部继承PG变量并禁用密码文件、
// service和用户证书发现；要求支持sslcertmode的PostgreSQL 17+客户端。
func (s *Service) dumpPostgres(ctx context.Context, destination string) error {
	tool := s.pgDumpPath
	var err error
	if tool == "" {
		tool, err = exec.LookPath("pg_dump")
		if err != nil {
			return ErrDumpMissing
		}
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrDump
	}
	defer output.Close()
	query := url.Values{"sslmode": {s.database.SSLMode}, "connect_timeout": {strconv.Itoa(s.database.ConnectTimeoutSeconds)}, "passfile": {os.DevNull}, "sslcertmode": {"disable"}, "sslrootcert": {filepath.Join(filepath.Dir(destination), "unused-root.crt")}}
	if s.database.SSLMode == "verify-ca" || s.database.SSLMode == "verify-full" {
		// libpq系统CA模式只接受verify-full；verify-ca在备份客户端上收紧为主机名验证。
		query.Set("sslrootcert", "system")
		query.Set("sslmode", "verify-full")
	}
	connection := &url.URL{Scheme: "postgresql", User: url.User(s.database.Username), Host: net.JoinHostPort(s.database.Host, strconv.Itoa(s.database.Port)), Path: "/" + s.database.Name, RawQuery: query.Encode()}
	cmd := exec.CommandContext(ctx, tool, "--format=custom", "--no-owner", "--no-privileges", "--no-password", "--dbname="+connection.String())
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "PG") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PGPASSWORD="+s.database.Password, "PGPASSFILE="+os.DevNull, "PGSERVICEFILE="+os.DevNull, "PGSYSCONFDIR="+filepath.Dir(destination))
	cmd.Stdout = &limitedWriter{writer: output, remaining: maxBackupBytes}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return ErrDumpMissing
		}
		return ErrDump
	}
	if err := output.Sync(); err != nil {
		return ErrDump
	}
	info, err := output.Stat()
	if err != nil || info.Size() < 5 || info.Size() > maxBackupBytes {
		return ErrDump
	}
	return nil
}

// archive通过各领域所有者的快照入口读取，不绕过文件锁和目录索引。
// 这些独立快照不代表与pg_dump处于同一个跨领域事务时点。
func (s *Service) archive(ctx context.Context, cfg backupConfig, trigger, directory string) (string, error) {
	for _, domain := range []string{"image_tasks", "images", "editable_files"} {
		if cfg.Include[domain] && s.snapshots[domain] == nil {
			return "", ErrSnapshot
		}
	}
	dumpPath := filepath.Join(directory, "application-database.pgdump")
	if err := s.dump(ctx, dumpPath); err != nil {
		return "", err
	}
	dump, err := os.Open(dumpPath)
	if err != nil {
		return "", ErrDump
	}
	defer dump.Close()
	info, err := dump.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 5 {
		return "", ErrDump
	}
	var header [5]byte
	if _, err = io.ReadFull(dump, header[:]); err != nil || string(header[:]) != "PGDMP" {
		return "", ErrDump
	}
	if _, err = dump.Seek(0, io.SeekStart); err != nil {
		return "", ErrDump
	}
	name := filepath.Join(directory, "backup.tar.gz")
	output, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", ErrDump
	}
	defer output.Close()
	compressed := &limitedWriter{writer: output, remaining: maxBackupBytes}
	gzipWriter := gzip.NewWriter(compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	defer gzipWriter.Close()
	defer tarWriter.Close()
	remaining := maxBackupBytes
	entries := 0
	seen := map[string]bool{}
	add := func(name string, size int64, reader io.Reader) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "../") || size < 0 || size > remaining || entries >= 100000 || seen[name] {
			return ErrLimit
		}
		remaining -= size
		entries++
		seen[name] = true
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size, ModTime: time.Now().UTC(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		n, err := io.Copy(tarWriter, &contextReader{ctx: ctx, reader: io.LimitReader(reader, size+1)})
		if err != nil || n != size {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrSnapshot
		}
		return nil
	}
	metadata := map[string]any{"version": 3, "created_at": time.Now().UTC().Format(time.RFC3339), "trigger": trigger, "app_version": s.version, "database_backend": "postgresql", "application_database": map[string]any{"type": "postgresql", "host": s.database.Host, "port": s.database.Port, "database": s.database.Name}}
	manifest, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", ErrDump
	}
	if err = add("backup-metadata.json", int64(len(manifest)), strings.NewReader(string(manifest))); err != nil {
		return "", err
	}
	if err = add("data/application-database.pgdump", info.Size(), dump); err != nil {
		return "", err
	}
	for _, domain := range []string{"image_tasks", "images", "editable_files"} {
		if !cfg.Include[domain] {
			continue
		}
		prefix := map[string]string{"image_tasks": "data/image_tasks/", "images": "data/images/", "editable_files": "data/files/"}[domain]
		ownerAdd := func(name string, size int64, reader io.Reader) error {
			if !strings.HasPrefix(name, prefix) {
				return ErrSnapshot
			}
			return add(name, size, reader)
		}
		if err := s.snapshots[domain](ctx, ownerAdd); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", ErrSnapshot
		}
	}
	if err = tarWriter.Close(); err != nil {
		return "", ErrDump
	}
	if err = gzipWriter.Close(); err != nil {
		return "", ErrDump
	}
	if err = output.Sync(); err != nil {
		return "", ErrDump
	}
	return name, nil
}
