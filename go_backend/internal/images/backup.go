package images

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
)

// BackupSnapshot 在资产操作锁下输出稳定领域快照，包含远端唯一副本。
func (s *ImageStorageService) BackupSnapshot(parent context.Context, add func(string, int64, io.Reader) error) error {
	if add == nil {
		return ErrAssets
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return e
	}
	defer done()
	for i := range s.assetLocks {
		s.assetLocks[i].Lock()
	}
	defer func() {
		for i := len(s.assetLocks) - 1; i >= 0; i-- {
			s.assetLocks[i].Unlock()
		}
	}()
	s.mu.Lock()
	snapshot := s.catalog.clone()
	s.mu.Unlock()
	data, e := json.Marshal(snapshot)
	if e != nil {
		return ErrAssets
	}
	if e = add("data/images/.gallery/catalog.json", int64(len(data)), bytes.NewReader(data)); e != nil {
		return e
	}
	paths := make([]string, 0, len(snapshot.Entries))
	for p := range snapshot.Entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, pending := snapshot.Tombstones[p]; pending {
			continue
		}
		entry := snapshot.Entries[p]
		data, e := s.bytesForEntry(ctx, p, entry)
		if e != nil {
			return e
		}
		if e = add("data/images/"+p, int64(len(data)), galleryContextReader{ctx: ctx, r: bytes.NewReader(data)}); e != nil {
			return e
		}
	}
	return nil
}
