package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrGalleryInput = errors.New("图库参数无效或超出限制")

type GalleryQuery struct {
	BaseURL                                    string
	Page, PageSize                             int
	MediaType, Tag, Search, StartDate, EndDate string
}
type GalleryRow struct {
	ID               string   `json:"id"`
	Path             string   `json:"path"`
	Filename         string   `json:"filename"`
	URL              string   `json:"url"`
	ThumbnailURL     string   `json:"thumbnail_url"`
	SizeBytes        int64    `json:"size_bytes"`
	CreatedAt        string   `json:"created_at"`
	Date             string   `json:"date"`
	MediaType        string   `json:"media_type"`
	Expired          bool     `json:"expired"`
	ExpiresAt        *string  `json:"expires_at"`
	ExpiresInSeconds *int64   `json:"expires_in_seconds"`
	Tags             []string `json:"tags"`
	Storage          string   `json:"storage"`
	Local            bool     `json:"local"`
	Webdav           bool     `json:"webdav"`
	Available        bool     `json:"available"`
	Width            *int     `json:"width"`
	Height           *int     `json:"height"`
	GenboxPush       any      `json:"genbox_push"`
}
type GalleryCapabilities struct {
	GenboxPush bool `json:"genbox_push"`
}
type GalleryMediaFacets struct {
	All   int `json:"all"`
	Image int `json:"image"`
}
type GalleryFacets struct {
	MediaTypes GalleryMediaFacets `json:"media_types"`
	Tags       []string           `json:"tags"`
}
type GalleryPage struct {
	SchemaVersion  int                 `json:"schema_version"`
	GeneratedAt    string              `json:"generated_at"`
	Items          []GalleryRow        `json:"items"`
	Total          int                 `json:"total"`
	TotalSizeBytes int64               `json:"total_size_bytes"`
	RetentionHours int                 `json:"retention_hours"`
	Capabilities   GalleryCapabilities `json:"capabilities"`
	Facets         GalleryFacets       `json:"facets"`
	MediaType      string              `json:"media_type"`
	Page           int                 `json:"page"`
	PageSize       int                 `json:"page_size"`
	PageCount      int                 `json:"page_count"`
	HasMore        bool                `json:"has_more"`
}
type GalleryCleanupResult struct {
	Removed          int    `json:"removed"`
	RemovedSizeBytes int64  `json:"removed_size_bytes"`
	RetentionHours   int    `json:"retention_hours"`
	Message          string `json:"message"`
}
type GalleryCleanupTargetResult struct {
	Removed       int    `json:"removed"`
	FreedMB       int64  `json:"freed_mb"`
	TargetFreeMB  int64  `json:"target_free_mb"`
	CurrentFreeMB int64  `json:"current_free_mb"`
	Done          bool   `json:"done"`
	DryRun        bool   `json:"dry_run"`
	Message       string `json:"message"`
}
type ImageStorageStats struct {
	DiskTotalMB    int64 `json:"disk_total_mb"`
	DiskUsedMB     int64 `json:"disk_used_mb"`
	DiskFreeMB     int64 `json:"disk_free_mb"`
	ImageCount     int   `json:"image_count"`
	ImageSizeMB    int64 `json:"image_size_mb"`
	ImageSizeBytes int64 `json:"image_size_bytes"`
}

// ConfigureRuntime 在应用组装阶段安装设置读取器。
func (s *ImageStorageService) ConfigureRuntime(runtime func(context.Context) (map[string]any, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = runtime
}
func (s *ImageStorageService) retention(ctx context.Context) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	s.mu.Lock()
	runtime := s.runtime
	s.mu.Unlock()
	if runtime == nil {
		return 24, nil
	}
	settings, e := runtime(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrAssets
	}
	value, ok := settings["image_retention_hours"]
	if !ok {
		return 24, nil
	}
	var hours int64
	switch v := value.(type) {
	case int:
		hours = int64(v)
	case int64:
		hours = v
	case float64:
		if math.Trunc(v) != v || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, ErrAssets
		}
		if v < 1 || v >= float64(math.MaxInt64) {
			return 0, ErrAssets
		}
		hours = int64(v)
	case json.Number:
		n, e := v.Int64()
		if e != nil {
			return 0, ErrAssets
		}
		hours = n
	default:
		return 0, ErrAssets
	}
	if hours < 1 {
		return 0, ErrAssets
	}
	return int(hours), nil
}

// 设置契约没有保留时长上限；较长时长不能因 time.Duration 溢出而误删资产。
func assetExpiry(created time.Time, hours int) time.Time {
	created = created.UTC()
	maximum := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if int64(hours) > (maximum.Unix()-created.Unix())/3600 {
		return maximum
	}
	return created.AddDate(0, 0, hours/24).Add(time.Duration(hours%24) * time.Hour)
}
func normalizeTags(tags []string) ([]string, error) {
	if len(tags) > 32 {
		return nil, ErrGalleryInput
	}
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		if !utf8.ValidString(tag) || len(tag) > 512 {
			return nil, ErrGalleryInput
		}
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > 64 || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return nil, ErrGalleryInput
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out, nil
}
func (s *ImageStorageService) UpdateTags(ctx context.Context, p string, tags []string) ([]string, error) {
	if !assetPath.MatchString(p) {
		return nil, ErrGalleryInput
	}
	tags, e := normalizeTags(tags)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.closed {
		return nil, ErrAssets
	}
	entry, ok := s.catalog.Entries[p]
	_, deleting := s.catalog.Tombstones[p]
	if !ok || deleting {
		return nil, ErrGalleryInput
	}
	next := s.catalog.clone()
	entry.Tags = tags
	next.Entries[p] = entry
	if e = s.commit(next); e != nil {
		return nil, e
	}
	return append([]string{}, tags...), nil
}
func (s *ImageStorageService) List(ctx context.Context, q GalleryQuery) (GalleryPage, error) {
	if q.Page < 0 || q.PageSize < 0 || len(q.Search) > 1024 || len(q.Tag) > 512 || (q.MediaType != "" && q.MediaType != "all" && q.MediaType != "image") || (q.StartDate != "" && !validDay(q.StartDate)) || (q.EndDate != "" && !validDay(q.EndDate)) || (q.StartDate != "" && q.EndDate != "" && q.StartDate > q.EndDate) {
		return GalleryPage{}, ErrGalleryInput
	}
	hours, e := s.retention(ctx)
	if e != nil {
		return GalleryPage{}, e
	}
	raw, e := s.runtimeSettings(ctx)
	if e != nil {
		return GalleryPage{}, e
	}
	cfg, e := storageSettings(raw)
	if e != nil {
		return GalleryPage{}, e
	}
	_, genboxError := genBoxSettings(raw)
	baseURL, e := s.ResolvePublicBaseURL(ctx, q.BaseURL)
	if e != nil {
		return GalleryPage{}, e
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 50
	}
	if q.PageSize > 500 {
		q.PageSize = 500
	}
	if q.MediaType == "" {
		q.MediaType = "all"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return GalleryPage{}, ctx.Err()
	}
	if s.closed {
		return GalleryPage{}, ErrAssets
	}
	now := time.Now().UTC()
	out := GalleryPage{SchemaVersion: 1, GeneratedAt: now.Format(time.RFC3339), Items: []GalleryRow{}, RetentionHours: hours, MediaType: q.MediaType, Page: q.Page, PageSize: q.PageSize, PageCount: 1, Facets: GalleryFacets{Tags: []string{}}}
	out.Capabilities.GenboxPush = genboxError == nil
	rows := []GalleryRow{}
	allTags := map[string]bool{}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	tag := strings.TrimSpace(q.Tag)
	for p, entry := range s.catalog.Entries {
		if _, deleting := s.catalog.Tombstones[p]; deleting {
			continue
		}
		for _, v := range entry.Tags {
			allTags[v] = true
		}
		day := p[:10]
		if (q.StartDate != "" && day < q.StartDate) || (q.EndDate != "" && day > q.EndDate) {
			continue
		}
		if tag != "" && tag != "all" {
			found := false
			for _, v := range entry.Tags {
				if v == tag {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		local, webdav := hasLocal(entry), hasRemote(entry)
		storage := "local"
		if local && webdav {
			storage = "both"
		} else if webdav {
			storage = "webdav"
		}
		if search != "" && !strings.Contains(strings.ToLower(p+" "+entry.CreatedAt.Format(time.RFC3339)+" "+storage+" "+strings.Join(entry.Tags, " ")), search) {
			continue
		}
		expires := assetExpiry(entry.CreatedAt, hours)
		expiresText := expires.Format(time.RFC3339)
		remaining := expires.Unix() - now.Unix()
		expired := !expires.After(now)
		if remaining < 0 {
			remaining = 0
		}
		available := local || (remoteCurrent(entry) && cfg.URL == entry.Remote.URL && cfg.Root == entry.Remote.Root)
		public := baseURL + "/images/" + p
		if cfg.PublicURL != "" {
			public = cfg.PublicURL + "/" + p
		}
		var receipt any
		if entry.Genbox != nil {
			v := *entry.Genbox
			receipt = &v
		}
		rows = append(rows, GalleryRow{ID: p, Path: p, Filename: path.Base(p), URL: public + "?v=" + entry.Generation, ThumbnailURL: baseURL + "/image-thumbnails/" + p + "?v=" + entry.Generation, SizeBytes: entry.Size, CreatedAt: entry.CreatedAt.Format(time.RFC3339), Date: day, MediaType: "image", Expired: expired, ExpiresAt: &expiresText, ExpiresInSeconds: &remaining, Tags: append([]string{}, entry.Tags...), Storage: storage, Local: local, Webdav: webdav, Available: available, GenboxPush: receipt, Width: &entry.Width, Height: &entry.Height})
		out.TotalSizeBytes += entry.Size
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt == rows[j].CreatedAt {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].CreatedAt > rows[j].CreatedAt
	})
	for tag := range allTags {
		out.Facets.Tags = append(out.Facets.Tags, tag)
	}
	sort.Strings(out.Facets.Tags)
	out.Total = len(rows)
	out.Facets.MediaTypes = GalleryMediaFacets{All: out.Total, Image: out.Total}
	out.PageCount = max(1, (out.Total+out.PageSize-1)/out.PageSize)
	out.Page = min(out.Page, out.PageCount)
	start := (out.Page - 1) * out.PageSize
	end := min(start+out.PageSize, len(rows))
	out.Items = append(out.Items, rows[start:end]...)
	out.HasMore = out.Page < out.PageCount
	return out, nil
}
func validatePaths(paths []string, limit int) ([]string, error) {
	if len(paths) == 0 || len(paths) > limit {
		return nil, ErrGalleryInput
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		if !assetPath.MatchString(p) || !validDay(p[:10]) {
			return nil, ErrGalleryInput
		}
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	return unique, nil
}
func (s *ImageStorageService) Delete(ctx context.Context, paths []string) (int, error) {
	paths, e := validatePaths(paths, 1000)
	if e != nil {
		return 0, e
	}
	s.mu.Lock()
	generations := map[string]string{}
	for _, p := range paths {
		if entry, ok := s.catalog.Entries[p]; ok {
			generations[p] = entry.Generation
		}
	}
	s.mu.Unlock()
	return s.deleteGenerations(ctx, generations)
}
func (s *ImageStorageService) removeFiles(p string, entry catalogEntry) error {
	if !assetPath.MatchString(p) || !generationPattern.MatchString(entry.Generation) {
		return ErrAssets
	}
	if i, e := s.root.Lstat(p); e == nil {
		if !i.Mode().IsRegular() || !s.regularAsset(p) {
			return ErrAssets
		}
		if s.root.Remove(p) != nil {
			return ErrAssets
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return ErrAssets
	}
	if e := s.root.Remove(thumbnailPath(entry.Generation)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return ErrAssets
	}
	return nil
}
func (s *ImageStorageService) CleanupExpired(ctx context.Context) (GalleryCleanupResult, error) {
	hours, e := s.retention(ctx)
	if e != nil {
		return GalleryCleanupResult{}, e
	}
	s.mu.Lock()
	generations := map[string]string{}
	for p, entry := range s.catalog.Entries {
		if !assetExpiry(entry.CreatedAt, hours).After(time.Now().UTC()) && len(generations) < 1000 {
			generations[p] = entry.Generation
		}
	}
	s.mu.Unlock()
	removed, size, e := s.deleteGenerationsMeasured(ctx, generations)
	if e != nil {
		return GalleryCleanupResult{}, e
	}
	return GalleryCleanupResult{Removed: removed, RemovedSizeBytes: size, RetentionHours: hours, Message: fmt.Sprintf("已清理 %d 个过期图片。", removed)}, nil
}
func (s *ImageStorageService) Storage(ctx context.Context) (ImageStorageStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ImageStorageStats{}, ctx.Err()
	}
	if s.closed {
		return ImageStorageStats{}, ErrAssets
	}
	return s.storageLocked()
}
func (s *ImageStorageService) storageLocked() (ImageStorageStats, error) {
	total, free, e := diskSpace(s.root)
	if e != nil {
		return ImageStorageStats{}, ErrAssets
	}
	out := ImageStorageStats{DiskTotalMB: total >> 20, DiskUsedMB: (total - free) >> 20, DiskFreeMB: free >> 20}
	for p, entry := range s.catalog.Entries {
		if _, pending := s.catalog.Tombstones[p]; pending {
			continue
		}
		if hasLocal(entry) {
			out.ImageCount++
			out.ImageSizeBytes += entry.Size
		}
	}
	out.ImageSizeMB = out.ImageSizeBytes >> 20
	return out, nil
}
func (s *ImageStorageService) CleanupToTarget(parent context.Context, targetFreeMb int64, dryRun bool) (GalleryCleanupTargetResult, error) {
	if targetFreeMb < 1 || targetFreeMb > math.MaxInt64/(1<<20) {
		return GalleryCleanupTargetResult{}, ErrGalleryInput
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return GalleryCleanupTargetResult{}, e
	}
	defer done()
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return GalleryCleanupTargetResult{}, e
	}
	s.mu.Lock()
	_, free, e := diskSpace(s.root)
	initial := free
	entries := map[string]catalogEntry{}
	paths := []string{}
	for p, entry := range s.catalog.Entries {
		_, pending := s.catalog.Tombstones[p]
		if !pending && hasLocal(entry) && (entry.Remote == nil || remoteCurrent(entry) && entry.Remote.URL == cfg.URL && entry.Remote.Root == cfg.Root) {
			entries[p] = entry
			paths = append(paths, p)
		}
	}
	s.mu.Unlock()
	if e != nil {
		return GalleryCleanupTargetResult{}, ErrAssets
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := entries[paths[i]], entries[paths[j]]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return paths[i] < paths[j]
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	out := GalleryCleanupTargetResult{TargetFreeMB: targetFreeMb, DryRun: dryRun}
	var freed int64
	for _, p := range paths {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if free >= targetFreeMb<<20 {
			break
		}
		entry := entries[p]
		s.mu.Lock()
		info, err := s.root.Lstat(p)
		valid := err == nil && info.Mode().IsRegular() && info.Size() == entry.Size && s.regularAsset(p)
		s.mu.Unlock()
		if !valid {
			continue
		}
		n := 0
		if dryRun {
			n = 1
		} else {
			lock := s.assetMutex(p)
			lock.Lock()
			n, e = s.removeLocalCopy(ctx, p, entry.Generation)
			lock.Unlock()
			if e != nil {
				return out, e
			}
		}
		if n > 0 {
			out.Removed += n
			freed += entry.Size
		}
		if dryRun {
			free = initial + freed
		} else {
			s.mu.Lock()
			_, free, e = diskSpace(s.root)
			s.mu.Unlock()
			if e != nil {
				return out, ErrAssets
			}
		}
	}
	out.FreedMB = freed >> 20
	out.CurrentFreeMB = free >> 20
	out.Done = free >= targetFreeMb<<20
	action := "已清理"
	if dryRun {
		action = "预计清理"
	}
	out.Message = action + " " + strconv.Itoa(out.Removed) + " 张本地图片。"
	return out, nil
}
