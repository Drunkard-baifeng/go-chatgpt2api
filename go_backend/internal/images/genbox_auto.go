package images

import (
	"context"
	"net/url"
	"strings"
)

type genBoxJob struct {
	Path, Generation string
	Metadata         map[string]string
}

// StartGenBoxAuto 显式安装有界队列，工作线程与资产服务共用关闭生命周期。
func (s *ImageStorageService) StartGenBoxAuto(workers, queueSize int) error {
	if workers < 1 || workers > 8 || queueSize < 1 || queueSize > 256 {
		return ErrStorageConfig
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.autoQueue != nil {
		return ErrStorageConfig
	}
	s.autoQueue = make(chan genBoxJob, queueSize)
	s.autoPending = map[string]string{}
	for range workers {
		s.autoWG.Add(1)
		go func() {
			defer s.autoWG.Done()
			for {
				select {
				case <-s.storageCtx.Done():
					return
				case job := <-s.autoQueue:
					_, _ = s.pushGenBox(s.storageCtx, job.Path, job.Metadata, job.Generation)
					s.mu.Lock()
					if s.autoPending[job.Path] == job.Generation {
						delete(s.autoPending, job.Path)
					}
					s.mu.Unlock()
				}
			}
		}()
	}
	return nil
}

func managedGalleryPath(raw string, bases []string) string {
	if len(raw) > 4096 {
		return ""
	}
	u, e := url.Parse(raw)
	if e != nil || u.Fragment != "" || u.User != nil {
		return ""
	}
	if u.RawQuery != "" {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 1 || len(query["v"]) != 1 || !generationPattern.MatchString(query.Get("v")) {
			return ""
		}
	}
	if u.Scheme == "" && u.Host == "" {
		p := strings.TrimPrefix(u.Path, "/images/")
		if p != u.Path && assetPath.MatchString(p) {
			return p
		}
		return ""
	}
	for _, base := range bases {
		b, e := url.Parse(strings.TrimRight(base, "/"))
		if e != nil || b.Scheme == "" || !strings.EqualFold(u.Host, b.Host) || u.Scheme != b.Scheme {
			continue
		}
		prefix := strings.TrimRight(b.Path, "/") + "/"
		if strings.HasPrefix(u.Path, prefix) {
			p := strings.TrimPrefix(u.Path, prefix)
			if assetPath.MatchString(p) {
				return p
			}
			if strings.HasPrefix(p, "images/") && assetPath.MatchString(strings.TrimPrefix(p, "images/")) {
				return strings.TrimPrefix(p, "images/")
			}
		}
	}
	return ""
}

// AutoPushGalleryURLs 只消费已登记受管 URL；容量不足时立即返回实际入队数。
func (s *ImageStorageService) AutoPushGalleryURLs(parent context.Context, urls []string, metadata map[string]string) int {
	if len(urls) > MaxN {
		return 0
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return 0
	}
	defer done()
	raw, e := s.runtimeSettings(ctx)
	if e != nil {
		return 0
	}
	cfg, e := genBoxSettings(raw)
	if e != nil || !cfg.Auto {
		return 0
	}
	storage, e := storageSettings(raw)
	if e != nil {
		return 0
	}
	copied := map[string]string{}
	for _, key := range []string{"prompt", "created_at", "date", "model"} {
		if value := metadata[key]; value != "" {
			if len(value) > 64<<10 {
				return 0
			}
			copied[key] = value
		}
	}
	baseURL, e := s.ResolvePublicBaseURL(ctx, "")
	if e != nil {
		return 0
	}
	bases := []string{baseURL, storage.PublicURL}
	count := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.autoQueue == nil {
		return 0
	}
	for _, raw := range urls {
		p := managedGalleryPath(raw, bases)
		entry, ok := s.catalog.Entries[p]
		_, deleting := s.catalog.Tombstones[p]
		if !ok || deleting || s.autoPending[p] == entry.Generation || entry.Genbox != nil && entry.Genbox.SHA256 == entry.SHA256 {
			continue
		}
		parsed, _ := url.Parse(raw)
		if revision := parsed.Query().Get("v"); revision != "" && revision != entry.Generation {
			continue
		}
		job := genBoxJob{Path: p, Generation: entry.Generation, Metadata: copied}
		select {
		case s.autoQueue <- job:
			s.autoPending[p] = entry.Generation
			count++
		default:
		}
	}
	return count
}
