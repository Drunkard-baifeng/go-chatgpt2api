package prompts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrStorage  = errors.New("提示词库存储暂不可用")
	ErrInput    = errors.New("提示词来源参数无效")
	ErrNotFound = errors.New("提示词来源不存在")
	ErrBusy     = errors.New("提示词库正在更新，请稍后重试")
	ErrClosed   = errors.New("提示词库服务已关闭")
)

// This static snapshot was explicitly copied from the same repository's
// Python default_prompt_library.json. Original source and author fields remain
// intact. Runtime reads only this embedded Go-owned copy.
//
//go:embed builtin.json
var builtin []byte

type Options struct {
	RegistryURL    string
	Timeout        time.Duration
	StorageTimeout time.Duration
}
type UpdateRequest struct {
	Enabled *bool `json:"enabled"`
}
type Service struct {
	db             *sql.DB
	base           string
	timeout        time.Duration
	storageTimeout time.Duration
	mu             sync.RWMutex
	state          snapshot
	settings       map[string]setting
	closed         bool
	lifetime       context.Context
	cancel         context.CancelFunc
	active         sync.WaitGroup
	mutation       chan struct{}
	fetch          func(context.Context, string, int64) ([]byte, error)
}

// New validates persistent state and seeds the bundled snapshot without network
// access. The constructor context bounds initialization, not service lifetime.
func New(ctx context.Context, db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, ErrStorage
	}
	base := strings.TrimRight(strings.TrimSpace(options.RegistryURL), "/")
	if base == "" {
		base = defaultRegistryURL
	}
	base = strings.TrimSuffix(base, "/manifest.json")
	parsed, err := validURL(base, false)
	if err != nil || parsed.RawQuery != "" {
		return nil, ErrInput
	}
	if options.Timeout <= 0 {
		options.Timeout = 120 * time.Second
	}
	if options.Timeout > 120*time.Second {
		return nil, ErrInput
	}
	if options.StorageTimeout <= 0 {
		options.StorageTimeout = 10 * time.Second
	}
	if options.StorageTimeout > 30*time.Second {
		return nil, ErrInput
	}
	lifetime, cancel := context.WithCancel(context.Background())
	s := &Service{db: db, base: base, timeout: options.Timeout, storageTimeout: options.StorageTimeout, lifetime: lifetime, cancel: cancel, mutation: make(chan struct{}, 1), fetch: fetchURL, settings: map[string]setting{}}
	if err := s.load(ctx); err != nil {
		cancel()
		return nil, err
	}
	if len(s.state.Items["banana-prompt-quicker"]) == 0 {
		// The bundled snapshot only fills an absent default source, never an
		// intentionally empty source from a complete verified remote registry.
		if _, present := s.state.Items["banana-prompt-quicker"]; !present {
			if err := seedBuiltin(&s.state); err != nil {
				cancel()
				return nil, err
			}
			if err := s.save(ctx, "snapshot", s.state); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	return s, nil
}
func (s *Service) load(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, s.storageTimeout)
	defer cancel()
	var settingsBytes, snapshotBytes []byte
	err := s.db.QueryRowContext(ctx, `SELECT settings,snapshot FROM prompt_library_state WHERE id=1`).Scan(&settingsBytes, &snapshotBytes)
	if errors.Is(err, sql.ErrNoRows) {
		settingsBytes = []byte(`{}`)
		snapshotBytes = []byte(`{}`)
	} else if err != nil {
		return storageError(ctx)
	}
	if len(settingsBytes) > 128<<10 || len(snapshotBytes) > 32<<20 {
		return ErrStorage
	}
	var rawSettings map[string]any
	if decodeJSON(settingsBytes, &rawSettings) != nil || rawSettings == nil {
		return ErrStorage
	}
	if rawSources, present := rawSettings["sources"]; present {
		sources, ok := rawSources.([]any)
		if !ok || len(sources) > maxSources {
			return ErrStorage
		}
		for index, value := range sources {
			raw, ok := value.(map[string]any)
			if !ok {
				return ErrStorage
			}
			id, _ := stringValue(raw, "id")
			if !sourceIDPattern.MatchString(id) {
				return ErrStorage
			}
			if _, exists := s.settings[id]; exists {
				return ErrStorage
			}
			config := sourceSetting(nil, id, index)
			if value, exists := raw["enabled"]; exists {
				enabled, ok := value.(bool)
				if !ok {
					return ErrStorage
				}
				config.Enabled = enabled
			}
			if value, exists := raw["sort_order"]; exists {
				n, ok := integer(value)
				if !ok || n < 0 {
					return ErrStorage
				}
				config.SortOrder = n
			}
			config.UpdatedAt, _ = stringValue(raw, "updated_at")
			s.settings[id] = config
		}
	}
	var rawSnapshot map[string]json.RawMessage
	if json.Unmarshal(snapshotBytes, &rawSnapshot) != nil || rawSnapshot == nil {
		return ErrStorage
	}
	if len(rawSnapshot) == 0 {
		s.state = snapshot{SchemaVersion: 1, Registry: registryState{Sources: defaultSources()}, Items: map[string][]Item{}, Status: map[string]sourceStatus{}}
		return nil
	}
	if json.Unmarshal(snapshotBytes, &s.state) != nil || s.state.SchemaVersion != 1 || len(s.state.Registry.Sources) == 0 || len(s.state.Registry.Sources) > maxSources || s.state.Items == nil || s.state.Status == nil {
		return ErrStorage
	}
	if s.state.Verified && (!hashPattern.MatchString(s.state.Registry.Revision) || s.state.Digest != snapshotDigest(s.state)) {
		return ErrStorage
	}
	known := map[string]bool{}
	total := 0
	for _, source := range s.state.Registry.Sources {
		if !sourceIDPattern.MatchString(source.ID) || known[source.ID] || source.Name == "" || len([]rune(source.Name)) > 120 || source.Count < 0 || !safePath(source.Path) {
			return ErrStorage
		}
		known[source.ID] = true
		if _, err := validURL(source.Homepage, false); err != nil {
			return ErrStorage
		}
		if _, err := validURL(source.UpstreamURL, false); err != nil {
			return ErrStorage
		}
		if s.state.Verified && !hashPattern.MatchString(source.SHA256) {
			return ErrStorage
		}
	}
	seen := map[string]bool{}
	for sourceID, items := range s.state.Items {
		if !known[sourceID] {
			return ErrStorage
		}
		total += len(items)
		if total > maxItems {
			return ErrStorage
		}
		for index, item := range items {
			if item.SourceID != sourceID || !strings.HasPrefix(item.ID, sourceID+":") || len([]rune(item.ID)) > 160 || seen[item.ID] || item.Title == "" || item.Prompt == "" || len(item.Prompt) > 512<<10 || len(item.Tags) > 24 || len(item.ReferenceImageURLs) > 12 {
				return ErrStorage
			}
			if item.ImageMode != "" && item.ImageMode != "generate" && item.ImageMode != "edit" {
				return ErrStorage
			}
			if item.ImageCount != nil && *item.ImageCount < 1 {
				return ErrStorage
			}
			if _, err := validURL(item.Preview, true); err != nil {
				return ErrStorage
			}
			if _, err := validURL(item.Link, true); err != nil {
				return ErrStorage
			}
			for _, reference := range item.ReferenceImageURLs {
				if _, err := validURL(reference, false); err != nil {
					return ErrStorage
				}
			}
			items[index] = cloneItem(item)
			seen[item.ID] = true
		}
	}
	for id, status := range s.state.Status {
		if !known[id] {
			return ErrStorage
		}
		if status.LastError != "" {
			status.LastError = ErrRegistry.Error()
		}
		if status.LastFetchMS != nil && *status.LastFetchMS < 0 {
			return ErrStorage
		}
		s.state.Status[id] = status
	}
	return nil
}

// save updates only the owned document and its revision under the existing
// domain advisory lock. Settings and snapshot never overwrite one another.
func (s *Service) save(parent context.Context, field string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, s.storageTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('prompt_library_state'))`); err != nil {
		return storageError(ctx)
	}
	var statement string
	if field == "settings" {
		statement = `INSERT INTO prompt_library_state(id,settings,snapshot,settings_revision,snapshot_revision,updated_at) VALUES(1,$1::jsonb,'{}'::jsonb,1,0,now()) ON CONFLICT(id) DO UPDATE SET settings=EXCLUDED.settings,settings_revision=prompt_library_state.settings_revision+1,updated_at=now() WHERE prompt_library_state.settings IS DISTINCT FROM EXCLUDED.settings`
	} else {
		statement = `INSERT INTO prompt_library_state(id,settings,snapshot,settings_revision,snapshot_revision,updated_at) VALUES(1,'{}'::jsonb,$1::jsonb,0,1,now()) ON CONFLICT(id) DO UPDATE SET snapshot=EXCLUDED.snapshot,snapshot_revision=prompt_library_state.snapshot_revision+1,updated_at=now() WHERE prompt_library_state.snapshot IS DISTINCT FROM EXCLUDED.snapshot`
	}
	if _, err = tx.ExecContext(ctx, statement, data); err != nil {
		return storageError(ctx)
	}
	if err = tx.Commit(); err != nil {
		return storageError(ctx)
	}
	return nil
}
func storageError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrStorage
}
func (s *Service) operation(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	s.active.Add(1)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	stop := context.AfterFunc(s.lifetime, cancel)
	return ctx, func() { stop(); cancel(); s.active.Done() }, nil
}
func (s *Service) View(ctx context.Context) (View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return View{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	return project(s.state, s.settings), nil
}
func (s *Service) UpdateSource(parent context.Context, id string, request UpdateRequest) (View, error) {
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return View{}, err
	}
	defer finish()
	select {
	case s.mutation <- struct{}{}:
		defer func() { <-s.mutation }()
	default:
		return View{}, ErrBusy
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	index := -1
	for i, source := range s.state.Registry.Sources {
		if source.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.RUnlock()
		return View{}, ErrNotFound
	}
	candidate := make(map[string]setting, len(s.settings))
	for id, value := range s.settings {
		candidate[id] = value
	}
	config := sourceSetting(candidate, id, index)
	sources := append([]registrySource{}, s.state.Registry.Sources...)
	s.mu.RUnlock()
	if request.Enabled != nil {
		config.Enabled = *request.Enabled
	}
	config.UpdatedAt = now()
	candidate[id] = config
	state := settingsState{SchemaVersion: 1, Sources: make([]setting, 0, len(sources))}
	for i, source := range sources {
		state.Sources = append(state.Sources, sourceSetting(candidate, source.ID, i))
	}
	if err := s.save(ctx, "settings", state); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = candidate
	return project(s.state, s.settings), nil
}
func (s *Service) Refresh(parent context.Context, id string) (View, error) {
	ctx, finish, err := s.operation(parent)
	if err != nil {
		return View{}, err
	}
	defer finish()
	select {
	case s.mutation <- struct{}{}:
		defer func() { <-s.mutation }()
	default:
		return View{}, ErrBusy
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	previous := s.state
	settings := s.settings
	known := id == ""
	for _, source := range previous.Registry.Sources {
		if source.ID == id {
			known = true
		}
	}
	s.mu.RUnlock()
	if !known {
		return View{}, ErrNotFound
	}
	started := time.Now()
	candidate, fetchErr := s.fetchCandidate(ctx)
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	elapsed := int(time.Since(started).Milliseconds())
	if fetchErr != nil {
		candidate = previous
		candidate.Status = make(map[string]sourceStatus, len(previous.Status))
		for key, status := range previous.Status {
			candidate.Status[key] = status
		}
		for index, source := range previous.Registry.Sources {
			if (id != "" && source.ID == id) || (id == "" && sourceSetting(settings, source.ID, index).Enabled) {
				status := candidate.Status[source.ID]
				status.LastError = ErrRegistry.Error()
				status.LastFetchMS = &elapsed
				candidate.Status[source.ID] = status
			}
		}
	} else {
		for _, source := range candidate.Registry.Sources {
			candidate.Status[source.ID] = sourceStatus{ContentRevision: source.SHA256, LastSyncAt: now(), LastFetchMS: &elapsed}
		}
	}
	candidate.UpdatedAt = now()
	candidate.Digest = snapshotDigest(candidate)
	if err := s.save(ctx, "snapshot", candidate); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = candidate
	return project(s.state, s.settings), nil
}
func (s *Service) fetchCandidate(ctx context.Context) (snapshot, error) {
	manifestBytes, err := s.fetch(ctx, s.base+"/manifest.json", manifestLimit)
	if err != nil {
		return snapshot{}, err
	}
	m, err := parseManifest(manifestBytes)
	if err != nil {
		return snapshot{}, err
	}
	// Always verify bytes against the manifest. A persisted "verified" flag is
	// never enough to skip validation of the currently served remote payload.
	payload, err := s.fetch(ctx, s.base+"/"+m.PromptsPath, payloadLimit)
	if err != nil {
		return snapshot{}, err
	}
	return parsePayload(m, payload)
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.active.Wait()
}

func seedBuiltin(state *snapshot) error {
	var raw struct {
		Prompts []map[string]any `json:"prompts"`
	}
	if decodeJSON(builtin, &raw) != nil || len(raw.Prompts) == 0 {
		return ErrStorage
	}
	var source registrySource
	for _, value := range state.Registry.Sources {
		if value.ID == "banana-prompt-quicker" {
			source = value
			break
		}
	}
	if source.ID == "" {
		return nil
	}
	items := []Item{}
	seen := map[string]bool{}
	for _, raw := range raw.Prompts {
		get := func(key string) string { value, _ := raw[key].(string); return strings.TrimSpace(value) }
		id, title, prompt := get("id"), cleanInline(get("title")), get("prompt")
		if title == "" || prompt == "" {
			return ErrStorage
		}
		if id == "" {
			digest := sha256.Sum256([]byte(title + "\n" + prompt))
			id = hex.EncodeToString(digest[:])[:16]
		}
		item := Item{ID: source.ID + ":" + id, SourceID: source.ID, SourceName: source.Name, Title: truncate(title, 200), Prompt: prompt, Description: truncate(cleanDisplay(get("description")), 700), Preview: resolveURL(source.UpstreamURL, get("preview")), Link: resolveURL(source.UpstreamURL, get("link")), Author: truncate(cleanInline(get("author")), 120), Category: truncate(cleanInline(get("category")), 80), SubCategory: truncate(cleanInline(get("sub_category")), 80), Tags: []string{}, ReferenceImageURLs: []string{}, ImageMode: imageMode(get("mode")), ImageModel: truncate(cleanInline(get("image_model")), 80), ImageSize: truncate(cleanInline(get("image_size")), 40), CreatedAt: get("created_at")}
		if item.Link == "" {
			item.Link = source.Homepage
		}
		if count, err := strconv.Atoi(get("image_count")); err == nil && count > 0 {
			item.ImageCount = &count
		}
		if tags, ok := raw["tags"]; ok {
			if values, err := stringList(tags, 24); err == nil {
				item.Tags = values
			}
		}
		if references, ok := raw["reference_image_urls"]; ok {
			if values, err := stringList(references, 12); err == nil {
				for _, value := range values {
					if ref := resolveURL(source.UpstreamURL, value); ref != "" {
						item.ReferenceImageURLs = append(item.ReferenceImageURLs, ref)
					}
				}
			}
		}
		if seen[item.ID] || len([]rune(item.ID)) > 160 {
			return ErrStorage
		}
		seen[item.ID] = true
		items = append(items, item)
	}
	state.Items[source.ID] = items
	zero := 0
	state.Status[source.ID] = sourceStatus{ContentRevision: contentRevision(items), LastSyncAt: "2026-06-22T00:00:00Z", LastFetchMS: &zero}
	state.UpdatedAt = now()
	state.Digest = snapshotDigest(*state)
	return nil
}
func resolveURL(base, raw string) string {
	if raw == "" {
		return ""
	}
	parsed, _ := url.Parse(base)
	value, err := parsed.Parse(raw)
	if err != nil {
		return ""
	}
	if _, err = validURL(value.String(), false); err != nil {
		return ""
	}
	return value.String()
}
