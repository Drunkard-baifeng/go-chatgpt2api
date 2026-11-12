package images

import (
	"context"
	"sort"
	"time"
)

type RetentionResult struct {
	Removed          int   `json:"removed"`
	Kept             int   `json:"kept"`
	RemovedSizeBytes int64 `json:"removed_size_bytes"`
	RetentionHours   int   `json:"retention_hours"`
	DryRun           bool  `json:"dry_run"`
}

// CleanupRetention 通过同一墓碑删除所有者收口；每次最多 1000 张，预览不写目录。
func (s *ImageStorageService) CleanupRetention(ctx context.Context, hours int, dryRun bool) (RetentionResult, error) {
	result := RetentionResult{RetentionHours: hours, DryRun: dryRun}
	if hours < 1 || hours > 876000 {
		return result, ErrGalleryInput
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return result, ctx.Err()
	}
	if s.closed {
		s.mu.Unlock()
		return result, ErrAssets
	}
	now := time.Now()
	paths := []string{}
	sizes := map[string]int64{}
	generations := map[string]string{}
	for p, entry := range s.catalog.Entries {
		if _, pending := s.catalog.Tombstones[p]; pending {
			continue
		}
		result.Kept++
		if !assetExpiry(entry.CreatedAt, hours).After(now) {
			paths = append(paths, p)
			sizes[p] = entry.Size
			generations[p] = entry.Generation
		}
	}
	sort.Strings(paths)
	if len(paths) > 1000 {
		paths = paths[:1000]
	}
	s.mu.Unlock()
	if dryRun {
		for _, p := range paths {
			result.Removed++
			result.RemovedSizeBytes += sizes[p]
		}
		result.Kept -= result.Removed
		return result, nil
	}
	for _, p := range paths {
		removed, err := s.deleteGenerations(ctx, map[string]string{p: generations[p]})
		if removed > 0 {
			result.Removed += removed
			result.Kept -= removed
			result.RemovedSizeBytes += sizes[p]
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
