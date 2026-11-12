package images

import (
	"context"
	"sort"
)

func (s *ImageStorageService) SyncWebDAV(parent context.Context) (ImageStorageSyncResult, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return ImageStorageSyncResult{}, e
	}
	defer done()
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return ImageStorageSyncResult{}, e
	}
	if cfg.Mode == "local" {
		return ImageStorageSyncResult{}, ErrStorageConfig
	}
	s.mu.Lock()
	entries := s.catalog.clone()
	s.mu.Unlock()
	paths := make([]string, 0, len(entries.Entries))
	for p := range entries.Entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := ImageStorageSyncResult{}
	for _, p := range paths {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		lock := s.assetMutex(p)
		lock.Lock()
		if intent, pending := entries.Tombstones[p]; pending {
			_, e = s.deleteAsset(ctx, p, intent.Generation)
			lock.Unlock()
			if e != nil {
				out.Failed++
			}
			continue
		}
		entry, e := s.snapshotEntry(p)
		if e != nil {
			lock.Unlock()
			out.Skipped++
			continue
		}
		if !hasLocal(entry) {
			lock.Unlock()
			out.Skipped++
			continue
		}
		if remoteCurrent(entry) {
			lock.Unlock()
			out.Skipped++
			continue
		}
		e = s.syncEntry(ctx, p, entry.Generation, cfg)
		lock.Unlock()
		if e != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Failed++
		} else {
			out.Uploaded++
		}
	}
	return out, nil
}

// syncEntry 先记录上传目标和待同步标记；中断与不确定响应会保留可恢复本地源。
func (s *ImageStorageService) syncEntry(ctx context.Context, p, generation string, cfg storageConfiguration) error {
	entry, e := s.snapshotEntry(p)
	if e != nil {
		return e
	}
	if entry.Generation != generation || !hasLocal(entry) {
		return ErrAssets
	}
	data, e := s.bytesForEntry(ctx, p, entry)
	if e != nil {
		return e
	}
	if entry.SHA256 == "" {
		entry.SHA256 = assetHash(data)
	}
	client, e := newWebDAV(cfg, entry.Remote)
	if e != nil {
		return e
	}
	defer client.close()
	s.mu.Lock()
	current, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if !ok || pending || current.Generation != generation {
		s.mu.Unlock()
		return ErrAssets
	}
	remote := remoteLocation{URL: cfg.URL, Root: cfg.Root, Pending: true}
	if current.Remote != nil {
		remote.SHA256 = current.Remote.SHA256
	}
	current.SHA256 = entry.SHA256
	current.Remote = &remote
	next := s.catalog.clone()
	next.Entries[p] = current
	e = s.commit(next)
	s.mu.Unlock()
	if e != nil {
		return e
	}
	if e = client.put(ctx, p, data); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok = s.catalog.Entries[p]
	_, pending = s.catalog.Tombstones[p]
	if !ok || pending || current.Generation != generation {
		return ErrAssets
	}
	remote.Pending = false
	remote.SHA256 = entry.SHA256
	current.Remote = &remote
	current.Location = "both"
	next = s.catalog.clone()
	next.Entries[p] = current
	return s.commit(next)
}
