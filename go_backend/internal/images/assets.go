package images

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/filelock"
)

var ErrAssets = errors.New("图片资产保存或读取失败")
var assetPath = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}/[a-f0-9]{32}\.(png|jpeg|webp|gif|svg)$`)

type ImageStorageService struct {
	root          *os.Root
	PublicBaseURL string
	mu            sync.Mutex
	catalog       assetCatalog
	release       func() error
	closed        bool
	closeDone     chan struct{}
	closeError    error
	runtime       func(context.Context) (map[string]any, error)
	storageCtx    context.Context
	storageCancel context.CancelFunc
	storageWG     sync.WaitGroup
	storageSlots  chan struct{}
	assetLocks    [64]sync.Mutex
	decodeMu      sync.Mutex
	autoQueue     chan genBoxJob
	autoPending   map[string]string
	autoWG        sync.WaitGroup
}

func NewImageStorageService(directory, publicURL string) (*ImageStorageService, error) {
	if directory == "" {
		return nil, ErrAssets
	}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(publicURL, "#") {
			return nil, ErrAssets
		}
		if u.Port() != "" {
			p, e := strconv.Atoi(u.Port())
			if e != nil || p < 1 || p > 65535 {
				return nil, ErrAssets
			}
		}
	}
	if os.MkdirAll(directory, 0700) != nil {
		return nil, ErrAssets
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrAssets
	}
	lifetime, cancel := context.WithCancel(context.Background())
	s := &ImageStorageService{root: root, PublicBaseURL: strings.TrimRight(publicURL, "/"), storageCtx: lifetime, storageCancel: cancel, storageSlots: make(chan struct{}, 4), closeDone: make(chan struct{})}
	for _, dir := range []string{".gallery", ".gallery/thumbnails", ".gallery/transfers"} {
		if root.MkdirAll(dir, 0700) != nil {
			root.Close()
			return nil, ErrAssets
		}
		info, e := root.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, ErrAssets
		}
	}
	s.release, err = filelock.Acquire(filepath.Join(directory, ".gallery", "writer.lock"))
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("%w：图片目录已被占用或无法加锁，请先停止其他后端进程并检查目录权限", ErrAssets)
	}
	if err = s.loadAndCoordinate(); err != nil {
		s.release()
		root.Close()
		return nil, ErrAssets
	}
	return s, nil
}
func (s *ImageStorageService) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeError
	}
	s.closed = true
	s.storageCancel()
	s.mu.Unlock()
	s.storageWG.Wait()
	s.autoWG.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.root.Close()
	releaseErr := s.release()
	if e != nil || releaseErr != nil {
		s.closeError = ErrAssets
	}
	close(s.closeDone)
	return s.closeError
}
func (s *ImageStorageService) Save(parent context.Context, data []byte) (string, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return "", e
	}
	defer done()
	input, e := Inspect(data)
	if e != nil {
		return "", e
	}
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return "", e
	}
	id, e := randomID()
	if e != nil {
		return "", e
	}
	p := time.Now().UTC().Format("2006-01-02") + "/" + id + "." + strings.TrimPrefix(input.Name, "image.")
	lock := s.assetMutex(p)
	lock.Lock()
	defer lock.Unlock()
	entry, e := newCatalogEntry(input, int64(len(data)), time.Now().UTC())
	if e != nil {
		return "", e
	}
	entry.SHA256 = assetHash(data)
	if cfg.Mode != "local" {
		entry.Remote = &remoteLocation{URL: cfg.URL, Root: cfg.Root, Pending: true}
	}
	s.mu.Lock()
	if s.closed || len(s.catalog.Entries) >= maxCatalogEntries {
		s.mu.Unlock()
		return "", ErrAssets
	}
	if e = s.writeAssetLocked(p, data); e == nil {
		next := s.catalog.clone()
		next.Entries[p] = entry
		e = s.commit(next)
	}
	s.mu.Unlock()
	if e != nil {
		return "", e
	}
	if cfg.Mode != "local" {
		client, e := newWebDAV(cfg, entry.Remote)
		if e != nil {
			return "", e
		}
		e = client.put(ctx, p, data)
		client.close()
		if e != nil {
			return "", e
		}
		s.mu.Lock()
		current, ok := s.catalog.Entries[p]
		_, deleting := s.catalog.Tombstones[p]
		if !ok || deleting || current.Generation != entry.Generation {
			s.mu.Unlock()
			return "", ErrAssets
		}
		remote := *current.Remote
		remote.SHA256 = current.SHA256
		remote.Pending = false
		current.Remote = &remote
		current.Location = "both"
		next := s.catalog.clone()
		next.Entries[p] = current
		e = s.commit(next)
		if e == nil && cfg.Mode == "webdav" {
			if e = s.root.Remove(p); e == nil {
				current.Location = "webdav"
				next = s.catalog.clone()
				next.Entries[p] = current
				e = s.commit(next)
			}
		}
		s.mu.Unlock()
		if e != nil {
			return "", ErrAssets
		}
	}
	return "/images/" + p, nil
}

func (s *ImageStorageService) writeAssetLocked(p string, data []byte) error {
	day := p[:10]
	if s.root.MkdirAll(day, 0700) != nil {
		return ErrAssets
	}
	info, e := s.root.Lstat(day)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrAssets
	}
	return s.atomicWrite(p, data)
}
func (s *ImageStorageService) Open(path string) (*AssetFile, error) {
	return s.OpenContext(context.Background(), path)
}
func (s *ImageStorageService) openLocked(path string) (*os.File, error) {
	if !assetPath.MatchString(path) {
		return nil, ErrAssets
	}
	entry, ok := s.catalog.Entries[path]
	_, deleting := s.catalog.Tombstones[path]
	if s.closed || !ok || deleting || !s.regularAsset(path) {
		return nil, ErrAssets
	}
	f, err := s.root.Open(path)
	if err != nil {
		return nil, ErrAssets
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		f.Close()
		return nil, ErrAssets
	}
	return f, nil
}
func (s *ImageStorageService) Discard(path string) {
	path = strings.TrimPrefix(path, "/images/")
	if assetPath.MatchString(path) {
		_, _ = s.Delete(context.Background(), []string{path})
	}
}
