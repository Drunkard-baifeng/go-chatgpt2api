package images

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/upstream"
	"chatgpt2api/go_backend/internal/usage"
)

var ErrBusy = errors.New("当前图片调用容量已满，请稍后重试")
var ErrDelivery = errors.New("图片响应交付失败")
var ErrUnavailable = errors.New("图片服务暂不可用")
var ErrOutput = errors.New("上游图片无效或整批图片超过 100 MiB")
var ErrIncomplete = errors.New("已有会话已查询完成，未提交的图片不会自动补发")

type AccountRepository interface {
	Select(context.Context, string) (accounts.Account, error)
}
type RecordRepository interface {
	Start(context.Context, callrecord.Record) error
	Finish(context.Context, callrecord.Record) error
}
type Session interface {
	GenerateImage(context.Context, string, upstream.ImageRequest) (upstream.ImageResult, error)
	Close()
}
type SessionFactory func(config.Upstream, string) (Session, error)
type Service struct {
	accounts AccountRepository
	records  RecordRepository
	assets   *ImageStorageService
	policy   config.Upstream
	factory  SessionFactory
	active   chan struct{}
	runtime  func(context.Context) (map[string]any, error)
}

func New(accounts AccountRepository, records RecordRepository, assets *ImageStorageService, policy config.Upstream, factory SessionFactory) *Service {
	if factory == nil {
		factory = func(p config.Upstream, proxy string) (Session, error) { return upstream.NewSession(p, proxy) }
	}
	return &Service{accounts: accounts, records: records, assets: assets, policy: policy, factory: factory, active: make(chan struct{}, 1)}
}

// ConfigureRuntime 仅在启动组装时调用；运行中的配置每次请求重新读取。
func (s *Service) ConfigureRuntime(runtime func(context.Context) (map[string]any, error)) {
	s.runtime = runtime
	s.active = make(chan struct{}, 32)
}

func (s *Service) ExecutionCapacity() int { return cap(s.active) }

type Output struct {
	RevisedPrompt string         `json:"revised_prompt"`
	URL           string         `json:"url,omitempty"`
	B64JSON       string         `json:"b64_json,omitempty"`
	Width         int            `json:"width,omitempty"`
	Height        int            `json:"height,omitempty"`
	Path          string         `json:"-"`
	Bytes         int            `json:"-"`
	Usage         map[string]any `json:"-"`
}

// TaskProgress 由图片执行者产生，任务队列负责持久化；不包含令牌。
type TaskSlot struct {
	AccountID string
	Recovery  upstream.ImageRecovery
	Completed bool
}

type TaskProgress struct {
	Slots     []TaskSlot `json:"Slots,omitempty"`
	AccountID string
	Slot      int
	Recovery  upstream.ImageRecovery
	Results   []Output
}
type TaskOptions struct {
	Progress     TaskProgress
	Resume       bool
	ExtraTimeout time.Duration
	Checkpoint   func(TaskProgress) error
}

// ExecuteTask 与同步调用共用准入、Lease、资产和调用记录生命周期。
func (s *Service) ExecuteTask(parent context.Context, subject auth.Subject, req Request, task TaskOptions, deliver func(context.Context, Result) error) (Result, error) {
	id := s.policy.AccountID
	if task.Resume && (!task.Progress.Resumable() || task.ExtraTimeout < 5*time.Second || task.ExtraTimeout > 120*time.Second) {
		return Result{}, ErrInput
	}
	if task.Checkpoint == nil {
		return Result{}, ErrInput
	}
	return s.execute(parent, subject, func(context.Context) (Request, error) { return req, nil }, id, deliver, &task)
}

type Result struct {
	Created int64          `json:"created"`
	Data    []Output       `json:"data"`
	Usage   map[string]any `json:"usage"`
}

// Execute owns the batch deadline and delivery; each image reserves its own account and session.
func (s *Service) Execute(parent context.Context, subject auth.Subject, read func(context.Context) (Request, error), deliver func(context.Context, Result) error) (Result, error) {
	return s.execute(parent, subject, read, s.policy.AccountID, deliver, nil)
}
func (s *Service) ExecuteForAccount(parent context.Context, subject auth.Subject, id string, read func(context.Context) (Request, error), deliver func(context.Context, Result) error) (Result, error) {
	return s.execute(parent, subject, read, id, deliver, nil)
}
func (s *Service) execute(parent context.Context, subject auth.Subject, read func(context.Context) (Request, error), accountID string, deliver func(context.Context, Result) error, task *TaskOptions) (result Result, err error) {
	policy := s.policy
	maxN := 4
	upscaleEnabled := false
	parallel, retryEnabled, maxAttempts := true, true, 4
	initialWait, interval := time.Duration(0), 3*time.Second
	settleEnabled, settleWindow := true, 5*time.Second
	removeConversation := false
	var gifModels []string
	if s.runtime != nil {
		values, e := s.runtime(parent)
		if e != nil {
			return result, ErrUnavailable
		}
		integer := func(key string, fallback int) int {
			if n, ok := values[key].(float64); ok && n >= 1 && n <= 86400 {
				return int(n)
			}
			return fallback
		}
		policy.ImageStreamSeconds = integer("image_stream_timeout_secs", policy.ImageStreamSeconds)
		policy.ImagePollSeconds = integer("image_poll_timeout_secs", policy.ImagePollSeconds)
		maxN = min(4, integer("image_max_n", 4))
		upscaleEnabled, _ = values["image_upscale_enabled"].(bool)
		if v, ok := values["image_parallel_generation"].(bool); ok {
			parallel = v
		}
		if v, ok := values["image_account_retry_enabled"].(bool); ok {
			retryEnabled = v
		}
		if n, ok := values["image_max_account_attempts"].(float64); ok && n >= 1 {
			maxInt := int(^uint(0) >> 1)
			if n >= float64(maxInt) {
				maxAttempts = maxInt
			} else {
				maxAttempts = int(n)
			}
		}
		if n, ok := values["image_poll_initial_wait_secs"].(float64); ok && n >= 0 && n <= 60 {
			initialWait = time.Duration(n * float64(time.Second))
		}
		if n, ok := values["image_poll_interval_secs"].(float64); ok && n >= .5 && n <= 60 {
			interval = time.Duration(n * float64(time.Second))
		}
		if v, ok := values["image_settle_enabled"].(bool); ok {
			settleEnabled = v
		}
		if n, ok := values["image_settle_secs"].(float64); ok && n >= .5 && n <= 86400 {
			settleWindow = time.Duration(n * float64(time.Second))
		}
		removeConversation, _ = values["image_remove_conversation_after_result"].(bool)
		switch models := values["gif_models"].(type) {
		case []string:
			gifModels = append([]string(nil), models...)
		case []any:
			for _, model := range models {
				if text, ok := model.(string); ok {
					gifModels = append(gifModels, text)
				}
			}
		}
	}
	if s.accounts == nil || s.records == nil || s.assets == nil || policy.ImageStreamSeconds < 1 || policy.ImagePollSeconds < 1 || policy.ImageStreamSeconds+policy.ImagePollSeconds > 86370 {
		return result, ErrUnavailable
	}
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		if observer, ok := s.records.(interface{ Rejected() }); ok {
			observer.Rejected()
		}
		return result, ErrBusy
	}
	budget := policy.ImageStreamSeconds + policy.ImagePollSeconds + 30
	if task != nil && task.Resume {
		policy.ImagePollSeconds = int(task.ExtraTimeout / time.Second)
		budget = policy.ImagePollSeconds + 30
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(budget)*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if read == nil {
		return result, ErrInput
	}
	req, err := read(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	if req.N > maxN && (task == nil || !task.Resume) {
		return result, ErrInput
	}
	check := req
	if task != nil && task.Resume {
		check.Edit = false
		check.Sources = nil
		check.Prompt = "查询已提交图片"
	}
	if err = validate(&check); err != nil {
		return result, err
	}
	if err = check.ValidateOutput(gifModels); err != nil {
		return result, err
	}
	req.UpstreamModel = check.UpstreamModel
	req.OutputFormat = check.OutputFormat
	req.BaseURL, err = s.assets.ResolvePublicBaseURL(ctx, req.BaseURL)
	if err != nil {
		return result, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, ErrUnavailable
	}
	started := time.Now()
	endpoint := "/v1/images/generations"
	if req.Edit {
		endpoint = "/v1/images/edits"
	}
	if req.Endpoint != "" {
		endpoint = req.Endpoint
	}
	record := callrecord.Record{ID: "call-" + hex.EncodeToString(random[:]), Subject: subject, Started: started, Model: req.Model, UpstreamModel: req.UpstreamModel, Endpoint: endpoint, Stage: "account_selection"}
	record.RequestText, record.RequestTextTruncated = callrecord.CaptureRequestText(req.Prompt)
	record.Trace = callrecord.NewTrace(callrecord.RequestMetadata{Size: req.Size, Quality: req.Quality, ResponseFormat: req.Format, OutputFormat: req.OutputFormat, N: req.N, Stream: req.Stream}, map[string]int{"images": len(req.Sources), "masks": len(req.MaskSources)})
	ctx = callrecord.WithTrace(ctx, record.Trace)
	record.RequestedCount = req.N
	if task != nil {
		record.Endpoint = "/api/image-tasks/generations"
		if req.Edit {
			record.Endpoint = "/api/image-tasks/edits"
		}
	}
	result = Result{Created: started.Unix(), Data: []Output{}}
	if !retryEnabled || accountID != "" {
		maxAttempts = 1
	}
	policy.TotalTimeoutSeconds = budget
	batch := &imageExecution{service: s, ctx: ctx, parent: parent, req: req, policy: policy, task: task, record: &record, parallel: parallel, maxAttempts: maxAttempts, upscale: upscaleEnabled, initialWait: initialWait, interval: interval, settleEnabled: settleEnabled, settleWindow: settleWindow, removeConversation: removeConversation, accepted: map[string]bool{}}
	if task != nil {
		batch.progress = task.Progress.clone()
	}
	if s.records.Start(ctx, record) != nil {
		return result, callrecord.ErrStorage
	}
	delivered := false
	defer func() {
		if cause := recover(); cause != nil {
			err = ErrUnavailable
			defer func() { panic(cause) }()
		}
		if err != nil && ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		if !delivered {
			for _, path := range batch.published {
				if !batch.accepted[path] {
					s.assets.Discard(path)
				}
			}
			if task == nil {
				result.Data = nil
				record.OutputCount = 0
			} else {
				result.Data = append([]Output{}, batch.progress.Results...)
				record.OutputCount = len(batch.accepted)
			}
		}
		diagnosticImages := []callrecord.ResultImage{}
		for _, image := range result.Data {
			publicURL := image.URL
			if publicURL == "" {
				base := strings.TrimRight(req.BaseURL, "/")
				publicURL = base + "/images/" + image.Path
			}
			diagnosticImages = append(diagnosticImages, callrecord.ResultImage{Width: image.Width, Height: image.Height, Bytes: image.Bytes, URL: publicURL})
		}
		record.Trace.SetResults(diagnosticImages)
		record.Ended = time.Now()
		record.Outcome = "success"
		record.APIStatusCode = 200
		if err != nil {
			record.Outcome = "failed"
			if errors.Is(err, context.Canceled) {
				record.Outcome = "cancelled"
			}
			record.ErrorCode = PublicError(err).Code
			record.PublicError = PublicError(err).Message
			record.APIStatusCode = PublicError(err).Status
			details := upstream.DiagnosticDetails(err)
			record.UpstreamError, record.UpstreamText, record.RawError = details.UpstreamError, details.UpstreamText, details.RawError
			var f *upstream.Failure
			if errors.As(err, &f) {
				record.Stage = f.Stage
			}
		} else {
			record.Stage = "completed"
			if record.OutputCount > 0 && record.OutputCount < record.RequestedCount {
				record.Outcome = "partial_success"
			}
		}
		finish, stop := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer stop()
		if s.records.Finish(finish, record) != nil {
			err = callrecord.ErrStorage
		}
	}()
	var inputDimensions [][2]int
	result.Data, inputDimensions, err = batch.run(accountID)
	if err != nil {
		return result, err
	}
	outputDimensions := make([][2]int, 0, len(result.Data))
	for i := range result.Data {
		dimensions := [2]int{result.Data[i].Width, result.Data[i].Height}
		if req.Format != "b64_json" {
			dimensions = [2]int{}
		}
		outputDimensions = append(outputDimensions, dimensions)
		result.Data[i].Usage = usage.Image(req.Prompt, req.Model, req.Size, req.Quality, inputDimensions, [][2]int{dimensions})
	}
	result.Usage = usage.Image(req.Prompt, req.Model, req.Size, req.Quality, inputDimensions, outputDimensions)
	record.Stage = "delivery"
	batch.notify()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if deliver != nil {
		done := callrecord.Span(ctx, "delivery", "response_ms")
		e := deliver(ctx, result)
		done(e)
		if e != nil {
			return result, ErrDelivery
		}
	}
	delivered = true
	record.OutputCount = len(result.Data)
	if task != nil && len(result.Data) > 0 {
		urls := make([]string, 0, len(result.Data))
		for _, output := range result.Data {
			urls = append(urls, "/images/"+output.Path)
		}
		s.assets.AutoPushGalleryURLs(ctx, urls, map[string]string{"prompt": req.Prompt, "model": req.Model})
	}
	return result, nil
}

type Error struct {
	Status        int
	Code, Message string
}

func PublicError(err error) Error {
	var f *upstream.Failure
	var transfer *assettransfer.Failure
	var schema *ParseError
	details := upstream.DiagnosticDetails(err)
	message := func(fallback string) string {
		if details.Message != "" {
			return callrecord.CaptureDiagnosticText(details.Message)
		}
		return fallback
	}
	switch {
	case errors.As(err, &schema):
		message := schema.Error()
		if len(schema.Errors) == 0 && schema.Field != "" {
			message = schema.Field + ": " + schema.Message
		}
		return Error{422, "bad_request", message}
	case errors.Is(err, ErrInput):
		return Error{400, "bad_request", err.Error()}
	case errors.As(err, &transfer) && transfer.Method == "PUT":
		status, code := transfer.Status, "upstream_error"
		switch {
		case status == 400:
			code = "invalid_image_input"
		case status == 429:
			code = "upstream_rate_limited"
		case status == 408 || status == 504 || transfer.TimedOut:
			code = "upstream_connection_timeout"
			if status < 400 {
				status = 504
			}
		case status == 403 || status == 423 || status >= 500:
			code = "upstream_unavailable"
		case status < 400:
			status, code = 502, "upstream_connection_failed"
		}
		return Error{status, code, message("The image generation tool encountered an error. Please try again.")}
	case errors.Is(err, ErrBusy), errors.Is(err, accounts.ErrBusy):
		return Error{429, "image_capacity_exceeded", ErrBusy.Error()}
	case errors.Is(err, ErrDelivery):
		return Error{502, "downstream_delivery_failed", ErrDelivery.Error()}
	case errors.Is(err, context.Canceled):
		return Error{499, "request_cancelled", "请求已取消"}
	case errors.Is(err, upstream.ErrImagePending):
		return Error{502, "image_poll_timeout", "Image generation timed out. Please try again."}
	case errors.Is(err, context.DeadlineExceeded):
		return Error{504, "image_timeout", "Image generation timed out. Please try again."}
	case errors.Is(err, accounts.ErrSelection):
		return Error{503, "upstream_account_unavailable", accounts.ErrSelection.Error()}
	case errors.Is(err, accounts.ErrQuotaExhausted):
		return Error{429, "image_quota_exhausted", "No image generation quota is currently available."}
	case errors.Is(err, accounts.ErrStorage):
		return Error{503, "account_storage_unavailable", accounts.ErrStorage.Error()}
	case errors.Is(err, callrecord.ErrStorage):
		return Error{503, "call_record_unavailable", callrecord.ErrStorage.Error()}
	case errors.Is(err, ErrAssets):
		return Error{503, "image_storage_unavailable", ErrAssets.Error()}
	case errors.Is(err, ErrUnavailable):
		return Error{503, "image_service_unavailable", ErrUnavailable.Error()}
	case errors.Is(err, upstream.ErrImageText):
		code := "upstream_text_reply"
		if errors.As(err, &f) && f.Code != "" {
			code = f.Code
		}
		return Error{400, code, message("The image generation tool encountered an error. Please try again.")}
	case errors.Is(err, ErrOutput):
		return Error{502, "invalid_upstream_image", ErrOutput.Error()}
	case errors.As(err, &f):
		status := 502
		if f.Status == 400 || f.Status == 401 || f.Status == 429 {
			status = f.Status
		}
		fallback := "上游拒绝请求，阶段：" + f.Stage + "（HTTP " + strconv.Itoa(f.Status) + "）"
		if f.Code == "image_quota_exhausted" || f.Code == "insufficient_quota" {
			fallback = "No image generation quota is currently available."
		}
		return Error{status, f.Code, message(fallback)}
	case errors.Is(err, assettransfer.ErrTransfer):
		return Error{502, "image_transfer_failed", assettransfer.ErrTransfer.Error()}
	case errors.Is(err, upstream.ErrResponseTooLarge):
		return Error{502, "upstream_response_too_large", "上游响应超过字节限制"}
	case errors.Is(err, upstream.ErrProtocol):
		return Error{502, "upstream_protocol_error", message("上游图片响应格式不受支持；详情中保留了协议诊断")}
	default:
		return Error{502, "upstream_unavailable", "上游图片服务暂不可用"}
	}
}

func (e Error) ErrorType() string {
	// 签名资源的 401/403 是传输失败，不能投影成调用者认证或权限失败。
	switch e.Code {
	case "upstream_error", "upstream_unavailable", "upstream_connection_failed", "upstream_connection_timeout":
		return "server_error"
	}
	if e.Code == "image_quota_exhausted" || e.Code == "insufficient_quota" {
		return "insufficient_quota"
	}
	if e.Status == 401 {
		return "authentication_error"
	}
	if e.Status == 429 {
		return "rate_limit_error"
	}
	if e.Status >= 400 && e.Status < 500 {
		return "invalid_request_error"
	}
	return "server_error"
}
