package images

import (
	"context"
	"sort"
)

// deleteGenerations 仅删除调用者观察到的代次，清理快照不拥有后续替换的资产。
func (s *ImageStorageService) deleteGenerations(parent context.Context, generations map[string]string) (int, error) {
	n, _, e := s.deleteGenerationsMeasured(parent, generations)
	return n, e
}
func (s *ImageStorageService) deleteGenerationsMeasured(parent context.Context, generations map[string]string) (int, int64, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return 0, 0, e
	}
	defer done()
	paths := make([]string, 0, len(generations))
	for p := range generations {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	removed := 0
	var removedSize int64
	for _, p := range paths {
		if ctx.Err() != nil {
			return removed, removedSize, ctx.Err()
		}
		lock := s.assetMutex(p)
		lock.Lock()
		s.mu.Lock()
		entry := s.catalog.Entries[p]
		s.mu.Unlock()
		n, e := s.deleteAsset(ctx, p, generations[p])
		lock.Unlock()
		removed += n
		if n > 0 {
			removedSize += entry.Size
		}
		if e != nil {
			return removed, removedSize, e
		}
	}
	return removed, removedSize, nil
}

func (s *ImageStorageService) deleteAsset(ctx context.Context, p, generation string) (int, error) {
	if !assetPath.MatchString(p) || !generationPattern.MatchString(generation) {
		return 0, ErrGalleryInput
	}
	s.mu.Lock()
	entry, ok := s.catalog.Entries[p]
	if !ok || entry.Generation != generation {
		s.mu.Unlock()
		return 0, nil
	}
	t, pending := s.catalog.Tombstones[p]
	if !pending || t.Generation != generation {
		id, e := randomID()
		if e != nil {
			s.mu.Unlock()
			return 0, e
		}
		t = assetTombstone{OperationID: id, Generation: generation}
		next := s.catalog.clone()
		next.Tombstones[p] = t
		if e = s.commit(next); e != nil {
			s.mu.Unlock()
			return 0, e
		}
	}
	s.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if entry.Remote != nil {
		cfg, e := s.storageConfig(ctx)
		if e != nil {
			return 0, e
		}
		client, e := newWebDAV(cfg, entry.Remote)
		if e != nil {
			return 0, e
		}
		e = client.delete(ctx, p)
		client.close()
		if e != nil {
			return 0, e
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.catalog.Entries[p]
	intent, exists := s.catalog.Tombstones[p]
	if !ok || current.Generation != generation || !exists || intent.OperationID != t.OperationID || intent.Generation != generation {
		return 0, nil
	}
	if e := s.removeFiles(p, current); e != nil {
		return 0, e
	}
	next := s.catalog.clone()
	delete(next.Entries, p)
	delete(next.Tombstones, p)
	if e := s.commit(next); e != nil {
		return 0, e
	}
	return 1, nil
}

// 磁盘阈值只回收本地副本，已有且与当前内容一致的远端副本继续可读。
func (s *ImageStorageService) removeLocalCopy(ctx context.Context, p, generation string) (int, error) {
	s.mu.Lock()
	entry, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if !ok || pending || entry.Generation != generation || !hasLocal(entry) {
		s.mu.Unlock()
		return 0, nil
	}
	if entry.Remote == nil {
		s.mu.Unlock()
		return s.deleteAsset(ctx, p, generation)
	}
	defer s.mu.Unlock()
	if !remoteCurrent(entry) {
		return 0, nil
	}
	if e := s.root.Remove(p); e != nil {
		return 0, ErrAssets
	}
	entry.Location = "webdav"
	next := s.catalog.clone()
	next.Entries[p] = entry
	if e := s.commit(next); e != nil {
		return 0, e
	}
	return 1, nil
}
