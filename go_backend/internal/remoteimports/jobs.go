package remoteimports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
)

type GroupBinding struct {
	RemoteGroupID string   `json:"remote_group_id"`
	Name          string   `json:"name"`
	AccountIDs    []string `json:"account_ids"`
}
type ImportRequest struct {
	Names               []string       `json:"names"`
	AccountIDs          []string       `json:"account_ids"`
	GroupBindings       []GroupBinding `json:"group_bindings"`
	CreateAccountGroups *bool          `json:"create_account_groups"`
	TargetGroup         *string        `json:"target_group_id"`
}
type Job struct {
	JobID             string              `json:"job_id"`
	Status            string              `json:"status"`
	Stage             string              `json:"stage"`
	StageLabel        string              `json:"stage_label"`
	StageTotal        int                 `json:"stage_total"`
	StageCompleted    int                 `json:"stage_completed"`
	Terminal          bool                `json:"terminal"`
	ProgressTotal     int                 `json:"progress_total"`
	ProgressCompleted int                 `json:"progress_completed"`
	StatusLabel       string              `json:"status_label"`
	Tone              string              `json:"tone"`
	Error             string              `json:"error"`
	SummaryItems      []map[string]any    `json:"summary_items"`
	ResultMessage     string              `json:"result_message"`
	ResultTone        string              `json:"result_tone"`
	CreatedAt         string              `json:"created_at"`
	UpdatedAt         string              `json:"updated_at"`
	Total             int                 `json:"total"`
	Completed         int                 `json:"completed"`
	Added             int                 `json:"added"`
	Skipped           int                 `json:"skipped"`
	Synced            int                 `json:"synced"`
	Failed            int                 `json:"failed"`
	FailedTotal       int                 `json:"failed_total"`
	Errors            []map[string]string `json:"errors"`
	Events            []map[string]any    `json:"events"`
}

func (j *Job) project() {
	j.Total = max(0, min(accounts.MaxBatch, j.Total))
	j.Completed = max(0, min(j.Total, j.Completed))
	j.FailedTotal = max(0, min(j.Total*2+1, j.FailedTotal))
	j.Failed = j.FailedTotal
	j.Added = max(0, min(j.Total, j.Added))
	j.Skipped = max(0, min(j.Total, j.Skipped))
	j.Synced = max(0, min(j.Total, j.Synced))
	j.Terminal = j.Status == "completed" || j.Status == "failed"
	if j.Terminal {
		j.Stage = "completed"
	}
	labels := map[string]string{"read_credentials": "读取凭据", "save_accounts": "保存账号", "sync_accounts": "同步账号与额度", "completed": "完成"}
	if labels[j.Stage] == "" {
		j.Stage = "read_credentials"
	}
	j.StageLabel = labels[j.Stage]
	j.StageTotal = max(0, min(accounts.MaxBatch, j.StageTotal))
	j.StageCompleted = max(0, min(j.StageTotal, j.StageCompleted))
	j.ProgressTotal = j.StageTotal
	j.ProgressCompleted = j.StageCompleted
	j.Tone = "info"
	j.StatusLabel = "处理中"
	j.ResultTone = "info"
	j.ResultMessage = ""
	if j.Status == "pending" {
		j.StatusLabel = "等待处理"
	}
	if j.Terminal {
		j.ProgressTotal = j.Total
		j.ProgressCompleted = j.Completed
		j.ResultMessage = "导入完成"
		j.StatusLabel = "处理完成"
		j.Tone = "success"
		if j.FailedTotal > 0 {
			j.StatusLabel = "部分失败"
			j.Tone = "warning"
			j.ResultMessage = "导入完成，部分账号处理失败"
		}
		if j.Status == "completed" {
			j.ProgressCompleted = j.Total
			j.Completed = j.Total
			j.StageTotal = j.Total
			j.StageCompleted = j.Total
		}
		if j.Status == "failed" {
			j.StatusLabel = "导入失败"
			j.Tone = "danger"
			if j.Error == "" {
				j.Error = "导入已中断；已保存账号保留"
			}
			j.ResultMessage = j.Error
		}
		j.ResultTone = j.Tone
	}
	j.SummaryItems = []map[string]any{}
	for _, v := range []struct {
		k, l string
		n    int
	}{{"added", "新增", j.Added}, {"skipped", "跳过", j.Skipped}, {"synced", "同步", j.Synced}, {"failed", "失败", j.FailedTotal}} {
		j.SummaryItems = append(j.SummaryItems, map[string]any{"key": v.k, "label": v.l, "value": v.n, "tone": j.Tone})
	}
	if j.Errors == nil {
		j.Errors = []map[string]string{}
	}
	if len(j.Errors) > 20 {
		j.Errors = j.Errors[len(j.Errors)-20:]
	}
	if j.Events == nil {
		j.Events = []map[string]any{}
	}
	if len(j.Events) > 100 {
		j.Events = j.Events[len(j.Events)-100:]
	}
}
func readJob(value any) *Job {
	m := object(value)
	if m == nil {
		return nil
	}
	j := &Job{JobID: bounded(m["job_id"], 128), Status: text(m["status"]), Stage: text(m["stage"]), Total: integer(m["total"]), Completed: integer(m["completed"]), Added: integer(m["added"]), Skipped: integer(m["skipped"]), Synced: integer(m["synced"]), FailedTotal: max(integer(m["failed_total"]), integer(m["failed"])), StageTotal: integer(m["stage_total"]), StageCompleted: integer(m["stage_completed"]), CreatedAt: bounded(m["created_at"], 64), UpdatedAt: bounded(m["updated_at"], 64)}
	if j.Status != "pending" && j.Status != "running" && j.Status != "completed" {
		j.Status = "failed"
	}
	if !jobIDPattern.MatchString(j.JobID) {
		j.JobID = ""
	}
	if _, err := time.Parse(time.RFC3339Nano, j.CreatedAt); err != nil {
		j.CreatedAt = now()
	}
	if _, err := time.Parse(time.RFC3339Nano, j.UpdatedAt); err != nil {
		j.UpdatedAt = j.CreatedAt
	}
	if j.StageTotal == 0 {
		j.StageTotal = j.Total
	}
	if raw, ok := m["errors"].([]any); ok {
		j.FailedTotal = max(j.FailedTotal, len(raw))
		for _, v := range raw {
			entry := object(v)
			stage := "fetch"
			if entry["stage"] == "sync" {
				stage = "sync"
			}
			j.Errors = append(j.Errors, map[string]string{"stage": stage, "name": "远程账号", "error": "账号读取或同步失败"})
		}
	}
	// 历史进度可能来自其他实现；只重建白名单事件，不回显远程错误或任意文本。
	if raw, ok := m["events"].([]any); ok {
		for _, v := range raw {
			entry := object(v)
			id := bounded(entry["account_id"], 64)
			if !managementID.MatchString(id) {
				continue
			}
			status := text(entry["status"])
			if status != "success" && status != "skipped" && status != "failed" {
				continue
			}
			action := text(entry["action"])
			if action != "import_account" && action != "sync_account" {
				continue
			}
			tone := "success"
			if status == "failed" {
				tone = "danger"
			}
			if status == "skipped" {
				tone = "warning"
			}
			j.Events = append(j.Events, map[string]any{"sequence": len(j.Events) + 1, "timestamp": bounded(entry["timestamp"], 64), "account_id": id, "account_label": id, "action": action, "status": status, "tone": tone, "message": "账号处理"})
		}
	}
	j.project()
	return j
}

var managementID = regexp.MustCompile(`^acct_[a-f0-9]{24}$`)
var jobIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var remoteID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func jobMap(j *Job) map[string]any {
	data, _ := json.Marshal(j)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Service) persist(ctx context.Context, provider, id string, j *Job) error {
	j.UpdatedAt = now()
	j.project()
	return s.update(ctx, func(all map[string][]map[string]any) error {
		for _, item := range all[provider] {
			if text(item["id"]) == id {
				old := object(item["import_job"])
				if text(old["job_id"]) != j.JobID {
					return ErrConflict
				}
				item["import_job"] = jobMap(j)
				return nil
			}
		}
		return ErrNotFound
	})
}
func selected(values []string, provider string) ([]string, error) {
	if len(values) == 0 || len(values) > accounts.MaxBatch {
		return nil, ErrInput
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, ErrInput
		}
		if provider == "sub2api" && !remoteID.MatchString(value) {
			return nil, ErrInput
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}
func (s *Service) target(ctx context.Context, target *string) error {
	if target == nil || strings.TrimSpace(*target) == "" {
		return nil
	}
	if len(*target) > 128 {
		return ErrInput
	}
	view, e := s.accounts.Groups(ctx)
	if e != nil {
		return e
	}
	for _, raw := range view["groups"].([]any) {
		if text(object(raw)["id"]) == strings.TrimSpace(*target) {
			return nil
		}
	}
	return accounts.ErrNotFound
}
func (s *Service) Start(parent context.Context, provider, id string, req ImportRequest) (*Job, error) {
	if !providerOK(provider) {
		return nil, ErrInput
	}
	values := req.Names
	if provider == "sub2api" {
		values = req.AccountIDs
	}
	ids, e := selected(values, provider)
	if e != nil {
		return nil, e
	}
	if (provider == "cpa" && (len(req.AccountIDs) > 0 || len(req.GroupBindings) > 0 || req.CreateAccountGroups != nil)) || (provider == "sub2api" && len(req.Names) > 0) {
		return nil, ErrInput
	}
	if len(req.GroupBindings) > accounts.MaxBatch {
		return nil, ErrInput
	}
	bindingIDs := map[string]bool{}
	bindingCount := 0
	allowed := map[string]bool{}
	for _, value := range ids {
		allowed[value] = true
	}
	for _, b := range req.GroupBindings {
		if len(b.RemoteGroupID) > 128 || len(b.Name) > 256 || len(b.AccountIDs) > accounts.MaxBatch {
			return nil, ErrInput
		}
		for _, aid := range b.AccountIDs {
			bindingCount++
			if bindingCount > accounts.MaxBatch || !allowed[aid] || bindingIDs[aid] {
				return nil, ErrInput
			}
			bindingIDs[aid] = true
		}
	}
	if e = s.target(parent, req.TargetGroup); e != nil {
		return nil, e
	}
	jobID, e := newID()
	if e != nil {
		return nil, e
	}
	j := &Job{JobID: jobID, Status: "pending", Stage: "read_credentials", StageTotal: len(ids), Total: len(ids), CreatedAt: now()}
	j.UpdatedAt = j.CreatedAt
	j.project()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	s.wg.Add(1)
	s.mu.Unlock()
	var source map[string]any
	e = s.update(parent, func(all map[string][]map[string]any) error {
		for _, items := range all {
			for _, item := range items {
				if active(item["import_job"]) {
					return ErrConflict
				}
			}
		}
		for _, item := range all[provider] {
			if text(item["id"]) == id {
				item["import_job"] = jobMap(j)
				source = item
				return nil
			}
		}
		return ErrNotFound
	})
	if e != nil {
		s.wg.Done()
		return nil, e
	}
	initial := readJob(jobMap(j))
	go func() { defer s.wg.Done(); s.run(provider, id, source, ids, req, j) }()
	return initial, nil
}
func (j *Job) failure(stage string, index int) {
	j.FailedTotal++
	j.Errors = append(j.Errors, map[string]string{"stage": stage, "name": fmt.Sprintf("第 %d 条账号", index+1), "error": "账号读取或同步失败"})
	j.project()
}
func retryBusy(ctx context.Context, fn func() error) error {
	for {
		e := fn()
		if !errors.Is(e, accounts.ErrBusy) {
			return e
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Service) groupBindings(ctx context.Context, req ImportRequest) (map[string]string, error) {
	out := map[string]string{}
	if req.TargetGroup != nil || (req.CreateAccountGroups != nil && !*req.CreateAccountGroups) {
		return out, nil
	}
	view, e := s.accounts.Groups(ctx)
	if e != nil {
		return nil, e
	}
	byName := map[string]string{}
	for _, raw := range view["groups"].([]any) {
		g := object(raw)
		byName[strings.ToLower(text(g["name"]))] = text(g["id"])
	}
	for _, b := range req.GroupBindings {
		if len(b.AccountIDs) == 0 {
			continue
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = strings.TrimSpace(b.RemoteGroupID)
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		gid := byName[key]
		if gid == "" {
			random, e := newID()
			if e != nil {
				return nil, e
			}
			gid = "sub2api-" + random[:16]
			e = retryBusy(ctx, func() error {
				_, err := s.accounts.SaveGroup(ctx, map[string]any{"id": gid, "name": name, "enabled": true, "create_only": true}, "")
				return err
			})
			if e != nil {
				return nil, e
			}
			byName[key] = gid
		}
		for _, aid := range b.AccountIDs {
			out[aid] = gid
		}
	}
	return out, nil
}
func (s *Service) run(provider, id string, source map[string]any, ids []string, req ImportRequest, j *Job) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()
	terminal := false
	defer func() {
		if recover() != nil {
			terminal = false
		}
		if !terminal {
			j.Status = "failed"
			j.Error = "导入已中断；已保存账号保留"
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = s.persist(cleanup, provider, id, j)
		}
	}()
	j.Status = "running"
	if s.persist(ctx, provider, id, j) != nil {
		return
	}
	m, e := newManagementClient(text(source["base_url"]))
	if e != nil {
		return
	}
	defer m.Close()
	var headers http.Header
	e = s.accounts.WithProcessing(ctx, func(ctx context.Context) error {
		var err error
		headers, err = m.auth(ctx, provider, source)
		return err
	})
	if e != nil {
		return
	}
	bindings, e := s.groupBindings(ctx, req)
	if e != nil {
		return
	}
	values := make([]map[string]any, len(ids))
	fetchErrors := make([]error, len(ids))
	if provider == "sub2api" {
		for start := 0; start < len(ids); start += 100 {
			end := min(start+100, len(ids))
			var batch map[string]map[string]any
			e = s.accounts.WithProcessing(ctx, func(ctx context.Context) error {
				var err error
				batch, err = fetchSubBatch(ctx, m, headers, ids[start:end])
				return err
			})
			if ctx.Err() != nil {
				return
			}
			if e == nil {
				for index := start; index < end; index++ {
					values[index] = batch[ids[index]]
				}
			}
		}
	}
	queue := make(chan int)
	results := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < min(8, len(ids)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range queue {
				if values[index] == nil {
					fetchErrors[index] = s.accounts.WithProcessing(ctx, func(ctx context.Context) error {
						var err error
						if provider == "cpa" {
							values[index], err = fetchCPA(ctx, m, headers, ids[index])
						} else {
							values[index], err = fetchSubDetail(ctx, m, headers, ids[index])
						}
						return err
					})
				}
				select {
				case results <- index:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(queue)
		for i := range ids {
			select {
			case queue <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	lastCheckpoint := time.Now()
	pending := 0
	for index := range results {
		j.Completed++
		j.StageCompleted = j.Completed
		if fetchErrors[index] != nil || values[index] == nil {
			j.failure("fetch", index)
		}
		pending++
		if pending >= 20 || time.Since(lastCheckpoint) > 250*time.Millisecond {
			if s.persist(ctx, provider, id, j) != nil {
				cancel()
				for range results {
				}
				return
			}
			pending = 0
			lastCheckpoint = time.Now()
		}
	}
	if ctx.Err() != nil {
		return
	}
	payloads := []map[string]any{}
	size := 0
	for i, value := range values {
		if value == nil || fetchErrors[i] != nil {
			continue
		}
		if req.TargetGroup != nil {
			value["group_id"] = strings.TrimSpace(*req.TargetGroup)
		} else if group := bindings[ids[i]]; group != "" {
			value["group_id"] = group
		}
		encoded, _ := json.Marshal(value)
		size += len(encoded)
		if size > accounts.MaxControlBytes {
			return
		}
		payloads = append(payloads, value)
	}
	j.Stage = "save_accounts"
	j.StageTotal = len(payloads)
	j.StageCompleted = 0
	if s.persist(ctx, provider, id, j) != nil {
		return
	}
	if len(payloads) > 0 {
		var result accounts.Operation
		e = retryBusy(ctx, func() error {
			var err error
			noItems := false
			result, err = s.accounts.Import(ctx, accounts.ImportRequest{Accounts: payloads, ReturnItems: &noItems})
			return err
		})
		if e != nil {
			return
		}
		j.Added = result.Added
		j.Skipped = result.Skipped
		j.Events = append(j.Events, result.Events...)
		j.Stage = "sync_accounts"
		j.StageTotal = len(result.UpdatedIDs)
		j.StageCompleted = 0
		if s.persist(ctx, provider, id, j) != nil {
			return
		}
		e = s.accounts.SyncImported(ctx, &result)
		j.Synced = result.Synced
		j.Events = result.Events
		for i := range result.Errors {
			j.failure("sync", i)
		}
		if e != nil {
			return
		}
	}
	j.Status = "completed"
	j.Stage = "completed"
	j.Completed = j.Total
	if s.persist(ctx, provider, id, j) != nil {
		return
	}
	terminal = true
}
