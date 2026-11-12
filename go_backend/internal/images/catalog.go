package images

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const maxCatalogBytes = 32 << 20
const maxCatalogEntries = 100000
const maxAssetBytes = 50 << 20
const catalogPath = ".gallery/catalog.json"

var generationPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type catalogEntry struct {
	Generation string           `json:"generation"`
	Size       int64            `json:"size"`
	CreatedAt  time.Time        `json:"created_at"`
	Width      int              `json:"width"`
	Height     int              `json:"height"`
	Tags       []string         `json:"tags"`
	Location   string           `json:"location,omitempty"`
	SHA256     string           `json:"sha256,omitempty"`
	Remote     *remoteLocation  `json:"remote,omitempty"`
	Genbox     *GenBoxPushState `json:"genbox_push,omitempty"`
}
type assetTombstone struct {
	OperationID string `json:"operation_id"`
	Generation  string `json:"generation"`
}
type assetCatalog struct {
	Version    int                       `json:"version"`
	Entries    map[string]catalogEntry   `json:"entries"`
	Tombstones map[string]assetTombstone `json:"tombstones"`
}

func (c assetCatalog) clone() assetCatalog {
	out := assetCatalog{Version: 1, Entries: make(map[string]catalogEntry, len(c.Entries)), Tombstones: make(map[string]assetTombstone, len(c.Tombstones))}
	for p, e := range c.Entries {
		e.Tags = append([]string{}, e.Tags...)
		if e.Remote != nil {
			remote := *e.Remote
			e.Remote = &remote
		}
		if e.Genbox != nil {
			receipt := *e.Genbox
			e.Genbox = &receipt
		}
		out.Entries[p] = e
	}
	for p, t := range c.Tombstones {
		out.Tombstones[p] = t
	}
	return out
}
func randomID() (string, error) {
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return "", ErrAssets
	}
	return hex.EncodeToString(id[:]), nil
}
func newCatalogEntry(input Input, size int64, created time.Time) (catalogEntry, error) {
	gen, e := randomID()
	return catalogEntry{Generation: gen, Size: size, CreatedAt: created, Width: input.Width, Height: input.Height, Tags: []string{}}, e
}

// atomicWrite 不截断已发布文件；所有路径均相对于 os.Root。
func (s *ImageStorageService) atomicWrite(name string, data []byte) error {
	id, e := randomID()
	if e != nil {
		return e
	}
	temp := name + "." + id + ".tmp"
	f, e := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrAssets
	}
	defer s.root.Remove(temp)
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return ErrAssets
	}
	if s.root.Rename(temp, name) != nil {
		return ErrAssets
	}
	return nil
}
func (s *ImageStorageService) commit(c assetCatalog) error {
	if len(c.Entries) > maxCatalogEntries || len(c.Tombstones) > maxCatalogEntries {
		return ErrAssets
	}
	data, e := json.Marshal(c)
	if e != nil || len(data) > maxCatalogBytes {
		return ErrAssets
	}
	if e = s.atomicWrite(catalogPath, data); e != nil {
		return e
	}
	s.catalog = c
	return nil
}
func (s *ImageStorageService) regularAsset(p string) bool {
	for _, name := range []string{path.Dir(p), p} {
		i, e := s.root.Lstat(name)
		if e != nil || i.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if name == p && !i.Mode().IsRegular() {
			return false
		}
	}
	return true
}
func (s *ImageStorageService) readAsset(p string) ([]byte, os.FileInfo, error) {
	if !assetPath.MatchString(p) || !s.regularAsset(p) {
		return nil, nil, ErrAssets
	}
	f, e := s.root.Open(p)
	if e != nil {
		return nil, nil, ErrAssets
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Size() < 1 || i.Size() > maxAssetBytes {
		return nil, nil, ErrAssets
	}
	b, e := io.ReadAll(io.LimitReader(f, maxAssetBytes+1))
	if e != nil || int64(len(b)) != i.Size() {
		return nil, nil, ErrAssets
	}
	return b, i, nil
}
func (s *ImageStorageService) loadAndCoordinate() error {
	s.catalog = assetCatalog{Version: 1, Entries: map[string]catalogEntry{}, Tombstones: map[string]assetTombstone{}}
	f, e := s.root.Open(catalogPath)
	if e == nil {
		linkInfo, linkErr := s.root.Lstat(catalogPath)
		if linkErr != nil || !linkInfo.Mode().IsRegular() {
			f.Close()
			return ErrAssets
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > maxCatalogBytes {
			f.Close()
			return ErrAssets
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxCatalogBytes+1))
		f.Close()
		if readErr != nil || len(data) > maxCatalogBytes {
			return ErrAssets
		}
		if rejectDuplicateCatalogKeys(data) != nil {
			return ErrAssets
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&s.catalog) != nil {
			return ErrAssets
		}
		var tail any
		if d.Decode(&tail) != io.EOF {
			return ErrAssets
		}
		if s.validateCatalog() != nil {
			return ErrAssets
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return ErrAssets
	}
	// 先收口匹配代次的删除，避免将尚未删除的文件重新纳入图库。
	for p, t := range s.catalog.Tombstones {
		entry, exists := s.catalog.Entries[p]
		if exists && entry.Generation == t.Generation {
			// 远端删除需等运行配置安装后显式重放，启动时保留隐藏墓碑。
			if entry.Remote != nil {
				continue
			}
			if s.removeFiles(p, entry) != nil {
				return ErrAssets
			}
			delete(s.catalog.Entries, p)
		}
		delete(s.catalog.Tombstones, p)
	}
	next := s.catalog.clone()
	// 仅枚举一层日期目录，并限制访问的目录项总数。
	root, e := s.root.Open(".")
	if e != nil {
		return ErrAssets
	}
	defer root.Close()
	visited := 0
	for {
		dirs, readErr := root.ReadDir(128)
		for _, dir := range dirs {
			visited++
			if visited > 2*maxCatalogEntries {
				return ErrAssets
			}
			if !dir.IsDir() || dir.Type()&os.ModeSymlink != 0 || !validDay(dir.Name()) {
				continue
			}
			day, e := s.root.Open(dir.Name())
			if e != nil {
				return ErrAssets
			}
			for {
				files, readErr := day.ReadDir(128)
				for _, file := range files {
					visited++
					if visited > 2*maxCatalogEntries {
						day.Close()
						return ErrAssets
					}
					p := dir.Name() + "/" + file.Name()
					if !assetPath.MatchString(p) || !file.Type().IsRegular() {
						continue
					}
					data, info, e := s.readAsset(p)
					if e != nil {
						day.Close()
						return e
					}
					old, exists := next.Entries[p]
					if exists && old.Size == info.Size() && (old.SHA256 == "" || old.SHA256 == assetHash(data)) {
						old.SHA256 = assetHash(data)
						if old.Location == "webdav" {
							old.Location = "both"
						}
						next.Entries[p] = old
						continue
					}
					input, e := Inspect(data)
					if e != nil {
						day.Close()
						return ErrAssets
					}
					entry, e := newCatalogEntry(input, info.Size(), info.ModTime().UTC())
					if e != nil {
						day.Close()
						return e
					}
					if exists {
						entry.Tags = old.Tags
						entry.Remote = old.Remote
						if entry.Remote != nil {
							remote := *entry.Remote
							remote.Pending = true
							entry.Remote = &remote
						}
						if e := s.root.Remove(thumbnailPath(old.Generation)); e != nil && !errors.Is(e, os.ErrNotExist) {
							day.Close()
							return ErrAssets
						}
					}
					entry.SHA256 = assetHash(data)
					next.Entries[p] = entry
					if len(next.Entries) > maxCatalogEntries {
						day.Close()
						return ErrAssets
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					day.Close()
					return ErrAssets
				}
			}
			day.Close()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ErrAssets
		}
	}
	for p, entry := range next.Entries {
		if !s.regularAsset(p) {
			if _, e := s.root.Lstat(p); errors.Is(e, os.ErrNotExist) {
				if entry.Remote != nil {
					// 删除已移除本地文件但尚未提交终态时，保留同代次墓碑等待显式重放。
					if intent, pending := next.Tombstones[p]; pending && intent.Generation == entry.Generation {
						continue
					}
					if !remoteCurrent(entry) {
						return ErrAssets
					}
					entry.Location = "webdav"
					next.Entries[p] = entry
					continue
				}
				delete(next.Entries, p)
				_ = s.root.Remove(thumbnailPath(entry.Generation))
			} else {
				return ErrAssets
			}
		}
	}
	if s.cleanTransferTemps() != nil {
		return ErrAssets
	}
	if s.cleanOrphanThumbnails(next) != nil {
		return ErrAssets
	}
	return s.commit(next)
}

// JSON 重复键（包括重复资产路径）存在持久状态歧义，必须拒绝。
func rejectDuplicateCatalogKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 8 {
			return ErrAssets
		}
		token, e := decoder.Token()
		if e != nil {
			return ErrAssets
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					return ErrAssets
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return ErrAssets
				}
				keys[name] = true
				if e = visit(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for decoder.More() {
				if e := visit(depth + 1); e != nil {
					return e
				}
			}
		default:
			return ErrAssets
		}
		_, e = decoder.Token()
		if e != nil {
			return ErrAssets
		}
		return nil
	}
	if e := visit(0); e != nil {
		return e
	}
	if _, e := decoder.Token(); e != io.EOF {
		return ErrAssets
	}
	return nil
}

func (s *ImageStorageService) cleanOrphanThumbnails(c assetCatalog) error {
	live := map[string]bool{}
	for _, entry := range c.Entries {
		live[entry.Generation+".jpeg"] = true
	}
	dir, e := s.root.Open(".gallery/thumbnails")
	if e != nil {
		return ErrAssets
	}
	defer dir.Close()
	visited := 0
	for {
		files, readErr := dir.ReadDir(128)
		for _, file := range files {
			visited++
			if visited > 2*maxCatalogEntries {
				return ErrAssets
			}
			name := file.Name()
			gen := strings.TrimSuffix(name, ".jpeg")
			if gen == name || !generationPattern.MatchString(gen) || live[name] {
				continue
			}
			if s.root.Remove(".gallery/thumbnails/"+name) != nil {
				return ErrAssets
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return ErrAssets
		}
	}
}
func (s *ImageStorageService) validateCatalog() error {
	c := s.catalog
	if c.Version != 1 || c.Entries == nil || c.Tombstones == nil || len(c.Entries) > maxCatalogEntries || len(c.Tombstones) > maxCatalogEntries {
		return ErrAssets
	}
	generations := map[string]bool{}
	for p, e := range c.Entries {
		maxPixels := int64(40_000_000)
		if strings.HasSuffix(p, ".svg") {
			maxPixels = 64_000_000
		}
		if !assetPath.MatchString(p) || !validDay(strings.SplitN(p, "/", 2)[0]) || !generationPattern.MatchString(e.Generation) || generations[e.Generation] || e.Size < 1 || e.Size > maxAssetBytes || e.Width < 1 || e.Height < 1 || e.Width > 40_000_000 || e.Height > 40_000_000 || int64(e.Width)*int64(e.Height) > maxPixels || (strings.HasSuffix(p, ".svg") && (e.Width > 8192 || e.Height > 8192 || e.Size > maxSVGBytes)) || e.CreatedAt.IsZero() || e.Tags == nil {
			return ErrAssets
		}
		tags, err := normalizeTags(e.Tags)
		if err != nil || len(tags) != len(e.Tags) {
			return ErrAssets
		}
		for i := range tags {
			if tags[i] != e.Tags[i] {
				return ErrAssets
			}
		}
		generations[e.Generation] = true
		if e.Location != "" && e.Location != "local" && e.Location != "webdav" && e.Location != "both" {
			return ErrAssets
		}
		if e.SHA256 != "" && !sha256Pattern.MatchString(e.SHA256) {
			return ErrAssets
		}
		if e.Remote != nil {
			remote := e.Remote
			if remote.URL == "" || remote.Root == "." || remote.SHA256 != "" && !sha256Pattern.MatchString(remote.SHA256) {
				return ErrAssets
			}
			cfg := storageConfiguration{URL: remote.URL, Root: remote.Root}
			client, err := newWebDAV(cfg, remote)
			if err != nil {
				return ErrAssets
			}
			client.close()
			root, err := cleanRoot(remote.Root)
			if err != nil || root != remote.Root {
				return ErrAssets
			}
			if e.SHA256 == "" {
				return ErrAssets
			}
		}
		if e.Location == "webdav" && !remoteCurrent(e) {
			return ErrAssets
		}
		if e.Genbox != nil && !validGenBoxState(e.Genbox) {
			return ErrAssets
		}
	}
	for p, t := range c.Tombstones {
		if !assetPath.MatchString(p) || !validDay(p[:10]) || !generationPattern.MatchString(t.Generation) || !generationPattern.MatchString(t.OperationID) {
			return ErrAssets
		}
	}
	return nil
}
func validDay(day string) bool {
	t, e := time.Parse("2006-01-02", day)
	return e == nil && t.Format("2006-01-02") == day
}

// 启动时只回收本服务随机命名的未完成传输，不触及管理员放置的其他文件。
func (s *ImageStorageService) cleanTransferTemps() error {
	dir, e := s.root.Open(".gallery/transfers")
	if e != nil {
		return ErrAssets
	}
	defer dir.Close()
	visited := 0
	for {
		files, readErr := dir.ReadDir(128)
		for _, file := range files {
			visited++
			if visited > 2*maxCatalogEntries {
				return ErrAssets
			}
			name := file.Name()
			if !strings.HasSuffix(name, ".tmp") || !generationPattern.MatchString(strings.TrimSuffix(name, ".tmp")) {
				continue
			}
			if !file.Type().IsRegular() {
				return ErrAssets
			}
			if s.root.Remove(".gallery/transfers/"+name) != nil {
				return ErrAssets
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return ErrAssets
		}
	}
}
