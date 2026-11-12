// Package imagetasks 拥有图片任务归属、幂等、调度和文件型持久化。
package imagetasks

import "time"

type Asset struct {
	URL           string `json:"url"`
	Path          string `json:"path"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
	Width         *int   `json:"width"`
	Height        *int   `json:"height"`
}
type Actions struct {
	ResumePoll bool `json:"resume_poll"`
}
type Row struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Terminal       bool    `json:"terminal"`
	Mode           string  `json:"mode"`
	Model          string  `json:"model"`
	Size           string  `json:"size"`
	Quality        string  `json:"quality"`
	StageCode      string  `json:"stage_code"`
	StageLabel     string  `json:"stage_label"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	RequestedCount int     `json:"requested_count"`
	SucceededCount int     `json:"succeeded_count"`
	FailedCount    int     `json:"failed_count"`
	PendingCount   int     `json:"pending_count"`
	DurationMS     *int64  `json:"duration_ms"`
	ElapsedMS      int64   `json:"elapsed_ms"`
	ErrorCode      string  `json:"error_code"`
	PublicError    string  `json:"public_error"`
	Results        []Asset `json:"results"`
	Actions        Actions `json:"actions"`
}
type Page struct {
	Items      []Row    `json:"items"`
	MissingIDs []string `json:"missing_ids"`
}

func terminal(status string) bool { return status != "queued" && status != "running" }
func (t *task) resumable() bool {
	return terminal(t.Status) && t.Status != "success" && t.Progress.execution().Resumable() && len(t.Progress.Results) < t.N
}
func (t *task) view() Row {
	r := Row{ID: t.ID, Status: t.Status, Terminal: terminal(t.Status), Mode: "generate", Model: t.Model, Size: t.Size, Quality: t.Quality, CreatedAt: t.Created.Format(time.RFC3339Nano), UpdatedAt: t.Updated.Format(time.RFC3339Nano), RequestedCount: t.N, SucceededCount: len(t.Progress.Results), ErrorCode: t.ErrorCode, PublicError: t.PublicError, Results: []Asset{}, Actions: Actions{ResumePoll: t.resumable()}}
	if t.Edit {
		r.Mode = "edit"
	}
	r.StageCode = t.Status
	r.StageLabel = map[string]string{"queued": "排队中", "running": "生成中", "success": "成功", "partial_success": "部分成功", "failed": "失败", "text_review": "文本"}[t.Status]
	if !r.Terminal && t.Progress.execution().Resumable() {
		r.StageCode = "generating"
		r.StageLabel = "上游生成中"
	}
	end := time.Now()
	if r.Terminal {
		end = t.Updated
	}
	r.ElapsedMS = max(int64(0), end.Sub(t.Created).Milliseconds())
	if r.Terminal {
		elapsed := r.ElapsedMS
		r.DurationMS = &elapsed
		if t.Status != "text_review" {
			r.FailedCount = t.N - r.SucceededCount
		}
	} else {
		r.PendingCount = t.N - r.SucceededCount
	}
	for _, a := range t.Progress.Results {
		a := a
		asset := Asset{URL: a.URL, Path: a.Path}
		if a.Width > 0 {
			asset.Width = &a.Width
		}
		if a.Height > 0 {
			asset.Height = &a.Height
		}
		r.Results = append(r.Results, asset)
	}
	return r
}
