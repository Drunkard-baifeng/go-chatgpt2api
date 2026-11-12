package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

func assetHash(data []byte) string      { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func hasLocal(entry catalogEntry) bool  { return entry.Location != "webdav" }
func hasRemote(entry catalogEntry) bool { return entry.Remote != nil && entry.Remote.SHA256 != "" }
func remoteCurrent(entry catalogEntry) bool {
	return hasRemote(entry) && !entry.Remote.Pending && entry.Remote.SHA256 == entry.SHA256
}
func (s *ImageStorageService) assetMutex(p string) *sync.Mutex {
	var hash uint32
	for i := range len(p) {
		hash = hash*31 + uint32(p[i])
	}
	return &s.assetLocks[hash%uint32(len(s.assetLocks))]
}

// 活动操作在关闭前注册；取消信号覆盖网络、读写与响应文件的生命周期。
func (s *ImageStorageService) beginStorage(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrAssets
	}
	select {
	case s.storageSlots <- struct{}{}:
	default:
		s.mu.Unlock()
		return nil, nil, ErrStorageBusy
	}
	s.storageWG.Add(1)
	lifetime := s.storageCtx
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	stop := context.AfterFunc(lifetime, cancel)
	var once sync.Once
	done := func() { once.Do(func() { stop(); cancel(); <-s.storageSlots; s.storageWG.Done() }) }
	if ctx.Err() != nil {
		done()
		return nil, nil, ctx.Err()
	}
	return ctx, done, nil
}

// AssetFile 为本地文件与远端临时文件提供相同的可寻址读取接口。
type AssetFile struct {
	file   *os.File
	remove func()
	done   func()
	once   sync.Once
}

func (f *AssetFile) Read(p []byte) (int, error)                   { return f.file.Read(p) }
func (f *AssetFile) Seek(offset int64, whence int) (int64, error) { return f.file.Seek(offset, whence) }
func (f *AssetFile) Stat() (os.FileInfo, error)                   { return f.file.Stat() }
func (f *AssetFile) Close() error {
	var e error
	f.once.Do(func() {
		e = f.file.Close()
		if f.remove != nil {
			f.remove()
		}
		if f.done != nil {
			f.done()
		}
	})
	return e
}

func (s *ImageStorageService) snapshotEntry(p string) (catalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if s.closed || !assetPath.MatchString(p) || !ok || pending {
		return catalogEntry{}, ErrAssets
	}
	return entry, nil
}
func (s *ImageStorageService) bytesForEntry(ctx context.Context, p string, entry catalogEntry) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if hasLocal(entry) {
		s.mu.Lock()
		data, _, e := s.readAsset(p)
		s.mu.Unlock()
		if e == nil && int64(len(data)) == entry.Size && (entry.SHA256 == "" || assetHash(data) == entry.SHA256) {
			return data, nil
		}
		return nil, ErrAssets
	}
	if !remoteCurrent(entry) {
		return nil, ErrAssets
	}
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return nil, e
	}
	client, e := newWebDAV(cfg, entry.Remote)
	if e != nil {
		return nil, e
	}
	defer client.close()
	data, e := client.get(ctx, p)
	if e != nil {
		return nil, e
	}
	if int64(len(data)) != entry.Size || assetHash(data) != entry.SHA256 {
		return nil, ErrStorageRemote
	}
	input, e := Inspect(data)
	if e != nil || input.Width != entry.Width || input.Height != entry.Height {
		return nil, ErrStorageRemote
	}
	return data, nil
}
func (s *ImageStorageService) tempAsset(ctx context.Context, data []byte, done func()) (*AssetFile, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrAssets
	}
	id, e := randomID()
	if e != nil {
		s.mu.Unlock()
		return nil, e
	}
	name := ".gallery/transfers/" + id + ".tmp"
	file, e := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	s.mu.Unlock()
	if e != nil {
		return nil, ErrAssets
	}
	cleanup := func() { _ = s.root.Remove(name) }
	if _, e = io.Copy(file, galleryContextReader{ctx: ctx, r: bytes.NewReader(data)}); e != nil {
		file.Close()
		cleanup()
		return nil, e
	}
	if _, e = file.Seek(0, 0); e != nil {
		file.Close()
		cleanup()
		return nil, ErrAssets
	}
	out := &AssetFile{file: file, remove: cleanup, done: done}
	context.AfterFunc(ctx, func() { _ = out.Close() })
	if ctx.Err() != nil {
		out.Close()
		return nil, ctx.Err()
	}
	return out, nil
}
func (s *ImageStorageService) OpenContext(parent context.Context, p string) (*AssetFile, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			done()
		}
	}()
	lock := s.assetMutex(p)
	lock.Lock()
	defer lock.Unlock()
	entry, e := s.snapshotEntry(p)
	if e != nil {
		return nil, e
	}
	if strings.HasSuffix(p, ".svg") {
		data, e := s.bytesForEntry(ctx, p, entry)
		if e != nil {
			return nil, e
		}
		if _, _, valid := svgDimensions(data); !valid {
			return nil, ErrAssets
		}
		out, e := s.tempAsset(ctx, data, done)
		if e != nil {
			return nil, e
		}
		success = true
		return out, nil
	}
	if hasLocal(entry) {
		s.mu.Lock()
		file, e := s.openLocked(p)
		s.mu.Unlock()
		if e != nil {
			return nil, e
		}
		out := &AssetFile{file: file, done: done}
		context.AfterFunc(ctx, func() { _ = out.Close() })
		if ctx.Err() != nil {
			out.Close()
			return nil, ctx.Err()
		}
		success = true
		return out, nil
	}
	data, e := s.bytesForEntry(ctx, p, entry)
	if e != nil {
		return nil, e
	}
	out, e := s.tempAsset(ctx, data, done)
	if e != nil {
		return nil, e
	}
	success = true
	return out, nil
}
