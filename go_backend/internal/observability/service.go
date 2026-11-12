// Package observability 拥有日志展示、实时活动窗口和可重建的概览指标。
package observability

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/settings"
)

const activeCapacity = 1024
const recentCapacity = 200

var beijing = time.FixedZone("Asia/Shanghai", 8*3600)

type Options struct {
	Version       string
	Started       time.Time
	DataDirectory string
}
type trackedCall struct {
	record        callrecord.Record
	events        []any
	persistFailed bool
	stageStarted  time.Time
	timings       map[string]int64
}
type Service struct {
	records      *callrecord.Repository
	accounts     *accounts.Repository
	settings     *settings.Service
	db           *sql.DB
	options      Options
	runtime      *runtimeSampler
	mu           sync.RWMutex
	active       map[string]trackedCall
	recent       []trackedCall
	dropped      int64
	execution    map[string]int
	activeEgress func() int
	rejected     []time.Time
}

func New(records *callrecord.Repository, accountRepository *accounts.Repository, settingService *settings.Service, options Options) *Service {
	if options.Started.IsZero() {
		options.Started = time.Now()
	}
	s := &Service{records: records, accounts: accountRepository, settings: settingService, options: options, active: map[string]trackedCall{}, recent: []trackedCall{}}
	s.runtime = &runtimeSampler{options: options, read: func(ctx context.Context) runtimeReading { return readRuntime(ctx, options.DataDirectory) }}
	if records != nil {
		s.db = records.Database()
		records.SetLifecycle(s)
	}
	return s
}
func (s *Service) List(ctx context.Context, f callrecord.Filter, publicBaseURLs ...string) (map[string]any, error) {
	if s.records == nil {
		return nil, callrecord.ErrStorage
	}
	page, err := s.records.List(ctx, f)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, raw := range page.Items {
		item, e := projectCall(raw, false, publicBaseURLs...)
		if e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	return map[string]any{"items": items, "total": page.Total, "limit": page.Limit, "offset": page.Offset, "has_more": int64(page.Offset+len(page.Items)) < page.Total, "facets_scope": "filtered", "stats_scope": "filtered", "total_scope": "filtered", "facets": page.Facets, "stats": page.Stats}, nil
}
func (s *Service) Detail(ctx context.Context, id string, publicBaseURLs ...string) (map[string]any, error) {
	if s.records == nil {
		return nil, callrecord.ErrStorage
	}
	raw, err := s.records.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return projectCall(raw, true, publicBaseURLs...)
}
func (s *Service) Delete(ctx context.Context, ids []string) (map[string]any, error) {
	if err := callrecord.ValidateIDs(ids); err != nil {
		return nil, err
	}
	if s.records == nil {
		return nil, callrecord.ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, _, err := s.metricSnapshot(ctx, time.Now()); err != nil {
		return nil, canceled(ctx, err)
	}
	count, err := s.records.Delete(ctx, ids)
	return map[string]any{"removed": count}, err
}

func (s *Service) Started(record callrecord.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) >= activeCapacity {
		s.dropped++
		return
	}
	record.Outcome = "running"
	s.active[record.ID] = trackedCall{record: record, stageStarted: record.Started, timings: map[string]int64{}, events: []any{monitorEvent(record, "request_started", "请求开始", record.Started)}}
}
func (s *Service) Finished(record callrecord.Record, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tracked, exists := s.active[record.ID]
	delete(s.active, record.ID)
	if !exists {
		tracked = trackedCall{events: []any{}}
		for i, previous := range s.recent {
			if previous.record.ID == record.ID {
				tracked = previous
				s.recent = append(s.recent[:i], s.recent[i+1:]...)
				break
			}
		}
	}
	tracked.record = record
	tracked.persistFailed = err != nil
	if tracked.record.Ended.IsZero() {
		tracked.record.Ended = time.Now()
	}
	if len(tracked.events) < 64 {
		tracked.events = append(tracked.events, monitorEvent(record, "request_finished", "请求结束", tracked.record.Ended))
	}
	s.recent = append(s.recent, tracked)
	if len(s.recent) > recentCapacity {
		copy(s.recent, s.recent[len(s.recent)-recentCapacity:])
		s.recent = s.recent[:recentCapacity]
	}
}
func (s *Service) Progress(record callrecord.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tracked, ok := s.active[record.ID]
	if !ok {
		return
	}
	record.Outcome = "running"
	now := time.Now()
	if tracked.record.Stage != record.Stage {
		if metric := stageMetric(tracked.record.Stage); metric != "" {
			tracked.timings[metric] += max(0, now.Sub(tracked.stageStarted).Milliseconds())
		}
		tracked.stageStarted = now
	}
	if tracked.record.Stage != record.Stage && len(tracked.events) < 63 {
		tracked.events = append(tracked.events, monitorEvent(record, "stage_changed", stageLabel(record.Stage), time.Now()))
	}
	tracked.record = record
	s.active[record.ID] = tracked
}

func stageMetric(stage string) string {
	switch stage {
	case "image_poll_tasks_request":
		return "poll_request_ms"
	case "image_settle_wait":
		return "poll_wait_ms"
	case "image_cleanup":
		return "cleanup_ms"
	case "account_selection":
		return "account_wait_ms"
	case "image_inputs":
		return "input_ms"
	case "upstream", "image_upstream", "editable_generation", "editable_poll":
		return "upstream_ms"
	case "image_publish", "file_publish":
		return "publish_ms"
	case "image_processing":
		return "processing_ms"
	case "delivery":
		return "delivery_ms"
	default:
		return ""
	}
}
func (s *Service) Prepare(record callrecord.Record) callrecord.Record {
	record.Outcome = outcome(record.Outcome, record.ErrorCode)
	s.mu.RLock()
	defer s.mu.RUnlock()
	tracked, ok := s.active[record.ID]
	if !ok {
		return record
	}
	record.Timings = map[string]int64{}
	for key, value := range tracked.timings {
		record.Timings[key] = value
	}
	end := record.Ended
	if end.IsZero() {
		end = time.Now()
	}
	if metric := stageMetric(tracked.record.Stage); metric != "" {
		record.Timings[metric] += max(0, end.Sub(tracked.stageStarted).Milliseconds())
	}
	return record
}
func (s *Service) ActiveCount() int { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.active) }
func (s *Service) Monitor() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	active := []any{}
	recent := []any{}
	slow := []any{}
	stages := map[string]int{}
	ordered := make([]trackedCall, 0, len(s.active))
	for _, record := range s.active {
		ordered = append(ordered, record)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].record.Started.Before(ordered[j].record.Started) })
	for _, tracked := range ordered {
		projected := projectMonitor(tracked, now, false)
		active = append(active, projected)
		stages[text(object(projected["presentation"])["stage_text"])]++
		if now.Sub(tracked.record.Started) >= 120*time.Second {
			slow = append(slow, projected)
		}
	}
	for i := len(s.recent) - 1; i >= 0; i-- {
		projected := projectMonitor(s.recent[i], now, false)
		recent = append(recent, projected)
		if integer(projected["duration_ms"]) >= 120000 {
			slow = append(slow, projected)
		}
	}
	sort.SliceStable(slow, func(i, j int) bool {
		return integer(object(slow[i])["duration_ms"]) > integer(object(slow[j])["duration_ms"])
	})
	if len(slow) > 20 {
		slow = slow[:20]
	}
	summary := monitorSummary(active, recent, s.rejectionCount())
	if s.activeEgress != nil {
		summary["active_egress_count"] = s.activeEgress()
	}
	stageItems := []any{}
	keys := []string{}
	for label := range stages {
		keys = append(keys, label)
	}
	sort.Strings(keys)
	for _, label := range keys {
		stageItems = append(stageItems, map[string]any{"label": label, "count": stages[label]})
	}
	return map[string]any{"schema_version": 1, "updated_at": stamp(now), "threadpool": map[string]any{"tokens": 0}, "execution_capacity": s.execution, "tracking_dropped": s.dropped, "summary": summary, "active": active, "recent": recent, "slow": slow, "completed_window_text": formatWindow(len(s.recent)), "entry_queue_text": "满载直接拒绝，不排队", "active_stage_items": stageItems, "diagnostic_groups": diagnosticGroups(summary, s.execution, formatWindow(len(s.recent)))}
}
func (s *Service) MonitorDetail(id string, publicBaseURLs ...string) (map[string]any, error) {
	if id == "" || len(id) > 64 {
		return nil, callrecord.ErrInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if record, ok := s.active[id]; ok {
		return projectMonitor(record, time.Now(), true, publicBaseURLs...), nil
	}
	for i := len(s.recent) - 1; i >= 0; i-- {
		if s.recent[i].record.ID == id {
			return projectMonitor(s.recent[i], time.Now(), true, publicBaseURLs...), nil
		}
	}
	return nil, callrecord.ErrNotFound
}
func (s *Service) PersistTerminal(ctx context.Context, tx *sql.Tx, record callrecord.Record) error {
	return persistMetric(ctx, tx, record)
}
func canceled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, callrecord.ErrInput) {
		return err
	}
	return callrecord.ErrStorage
}
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
