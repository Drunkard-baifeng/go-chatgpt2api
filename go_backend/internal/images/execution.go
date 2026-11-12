package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/upstream"
)

// imageExecution owns the shared batch state. Network work happens outside mu;
// checkpoints are serialized with the state changes they durably acknowledge.
type imageExecution struct {
	service                           *Service
	ctx, parent                       context.Context
	req                               Request
	policy                            config.Upstream
	task                              *TaskOptions
	record                            *callrecord.Record
	parallel, upscale                 bool
	maxAttempts                       int
	initialWait, interval             time.Duration
	settleEnabled, removeConversation bool
	settleWindow                      time.Duration
	mu                                sync.Mutex
	selectionMu                       sync.Mutex
	activeAttempts                    int
	released                          chan struct{}
	progress                          TaskProgress
	published                         []string
	accepted                          map[string]bool
	total                             int
	inputDimensions                   [][2]int
}

func (p TaskProgress) clone() TaskProgress {
	p.Results = append([]Output{}, p.Results...)
	p.Recovery.Excluded = append([]string{}, p.Recovery.Excluded...)
	p.Slots = append([]TaskSlot(nil), p.Slots...)
	for i := range p.Slots {
		p.Slots[i].Recovery.Excluded = append([]string{}, p.Slots[i].Recovery.Excluded...)
	}
	return p
}

func (p TaskProgress) Resumable() bool {
	if len(p.Slots) == 0 {
		return p.AccountID != "" && p.Recovery.Valid()
	}
	for _, slot := range p.Slots {
		if !slot.Completed && slot.AccountID != "" && slot.Recovery.Valid() {
			return true
		}
	}
	return false
}

// Slots is authoritative for new tasks. Legacy single-slot task files remain readable.
func (p *TaskProgress) initialize(n int) error {
	if len(p.Slots) > 0 {
		if len(p.Slots) != n {
			return ErrInput
		}
		return nil
	}
	if p.Slot < 0 || p.Slot >= n || len(p.Results) > n {
		return ErrInput
	}
	if p.Recovery.Valid() && p.Slot < len(p.Results) {
		return ErrInput
	}
	p.Slots = make([]TaskSlot, n)
	for i := 0; i < len(p.Results); i++ {
		p.Slots[i].Completed = true
		p.Slots[i].AccountID = p.AccountID
	}
	if p.AccountID != "" {
		p.Slots[p.Slot].AccountID = p.AccountID
		p.Slots[p.Slot].Recovery = p.Recovery
	}
	return nil
}

func (b *imageExecution) notify() {
	if observer, ok := b.service.records.(interface{ Progress(callrecord.Record) }); ok {
		observer.Progress(*b.record)
	}
}
func (b *imageExecution) checkpoint(slot int) error {
	if b.task == nil {
		return nil
	}
	b.progress.Slot = slot
	b.progress.AccountID = b.progress.Slots[slot].AccountID
	b.progress.Recovery = b.progress.Slots[slot].Recovery
	// Keep the legacy projection useful for old diagnostics while all recovery lives in Slots.
	for i, s := range b.progress.Slots {
		if !s.Completed && s.Recovery.Valid() {
			b.progress.Slot = i
			b.progress.AccountID = s.AccountID
			b.progress.Recovery = s.Recovery
			break
		}
	}
	if e := b.task.Checkpoint(b.progress.clone()); e != nil {
		return errors.Join(upstream.ErrImageCheckpoint, e)
	}
	return nil
}

func (b *imageExecution) run(accountID string) ([]Output, [][2]int, error) {
	b.released = make(chan struct{})
	if e := b.progress.initialize(b.req.N); e != nil {
		return nil, nil, e
	}
	for _, output := range b.progress.Results {
		b.total += output.Bytes
	}
	outputs := make([]Output, b.req.N)
	failures := make([]error, b.req.N)
	panics := make([]any, b.req.N)
	indices := []int{}
	for i, slot := range b.progress.Slots {
		if slot.Completed {
			continue
		}
		if b.task != nil && b.task.Resume && (!slot.Recovery.Valid() || slot.AccountID == "") {
			continue
		}
		indices = append(indices, i)
	}
	var wg sync.WaitGroup
	for _, i := range indices {
		work := func() {
			defer wg.Done()
			defer func() { panics[i] = recover() }()
			outputs[i], failures[i] = b.generate(i, accountID)
		}
		wg.Add(1)
		if b.parallel && len(indices) > 1 {
			go work()
		} else {
			work()
		}
	}
	wg.Wait()
	for _, cause := range panics {
		if cause != nil {
			panic(cause)
		}
	}
	result := []Output{}
	if b.task != nil {
		result = append(result, b.progress.Results...)
	} else {
		for _, output := range outputs {
			if output.Path != "" {
				result = append(result, output)
			}
		}
	}
	if b.ctx.Err() != nil {
		return result, b.inputDimensions, errors.Join(append([]error{b.ctx.Err()}, failures...)...)
	}
	// Batch infrastructure failures abort delivery even if another slot succeeded.
	for _, failure := range failures {
		for _, fatal := range []error{context.Canceled, upstream.ErrImageCheckpoint, ErrAssets, ErrOutput, accounts.ErrStorage, callrecord.ErrStorage} {
			if errors.Is(failure, fatal) {
				return result, b.inputDimensions, failure
			}
		}
	}
	// Python returns successful slots from a partially successful synchronous batch.
	// Tasks retain their explicit partial terminal state and recovery checkpoints.
	if b.task == nil && len(result) > 0 {
		return result, b.inputDimensions, nil
	}
	for _, failure := range failures {
		if failure != nil {
			return result, b.inputDimensions, failure
		}
	}
	if len(result) < b.req.N {
		return result, b.inputDimensions, ErrIncomplete
	}
	return result, b.inputDimensions, nil
}

func (b *imageExecution) generate(slot int, accountID string) (Output, error) {
	excluded := []string{}
	var last error
	previousEmail := ""
	resume := b.task != nil && b.task.Resume
	if resume {
		b.mu.Lock()
		accountID = b.progress.Slots[slot].AccountID
		b.mu.Unlock()
	}
	for attempt := 0; attempt < b.maxAttempts; attempt++ {
		if b.ctx.Err() != nil {
			return Output{}, errors.Join(b.ctx.Err(), last)
		}
		selectionStarted := time.Now()
		selectionDone := callrecord.Span(b.ctx, "account_selection", "account_wait_ms")
		lease, e := b.acquire(accountID, excluded)
		selectionDone(e)
		if e != nil {
			if last != nil && (errors.Is(e, accounts.ErrSelection) || errors.Is(e, accounts.ErrQuotaExhausted) || errors.Is(e, accounts.ErrBusy)) {
				return Output{}, last
			}
			return Output{}, e
		}
		if len(excluded) > 0 {
			b.mu.Lock()
			b.record.Switches++
			b.mu.Unlock()
		}
		excluded = append(excluded, lease.Account.ID)
		output, e := b.attempt(slot, lease, resume, attempt+1, previousEmail, selectionStarted)
		previousEmail = lease.Account.Email
		if e == nil {
			return output, nil
		}
		last = e
		if b.ctx.Err() != nil {
			return Output{}, errors.Join(b.ctx.Err(), e)
		}
		if resume || accountID != "" || !switchImageAccount(e) {
			return Output{}, e
		}
	}
	return Output{}, last
}

func (b *imageExecution) acquire(id string, excluded []string) (*accounts.Lease, error) {
	for {
		// Serialize only account selection so a sibling's reservation is visible
		// before interpreting capacity. Never wait for unrelated requests.
		b.selectionMu.Lock()
		var lease *accounts.Lease
		var err error
		if (b.task != nil && b.task.Resume) || b.req.OutputFormat == "svg" {
			lease, err = accounts.AcquireExcluding(b.ctx, b.service.accounts, id, b.req.OutputFormat != "svg", excluded)
		} else {
			source, plans := modelConstraints(b.req.Model)
			lease, err = accounts.AcquireImage(b.ctx, b.service.accounts, id, source, plans, excluded)
		}
		b.mu.Lock()
		if err == nil {
			b.activeAttempts++
		}
		wait, released := errors.Is(err, accounts.ErrBusy) && b.activeAttempts > 0, b.released
		b.mu.Unlock()
		b.selectionMu.Unlock()
		if !wait {
			return lease, err
		}
		select {
		case <-released:
		case <-b.ctx.Done():
			return nil, b.ctx.Err()
		}
	}
}

// Python's image_failure.switch_account follows the text/failure boundary, not
// retryable. Uncertain POSTs and local persistence failures remain non-replayable.
func switchImageAccount(err error) bool {
	for _, terminal := range []error{context.Canceled, upstream.ErrImageText, upstream.ErrImageSubmissionUnknown, upstream.ErrImageCheckpoint, ErrInput, ErrAssets, ErrOutput, ErrDelivery, accounts.ErrStorage} {
		if errors.Is(err, terminal) {
			return false
		}
	}
	var transfer *assettransfer.Failure
	if errors.As(err, &transfer) && transfer.Status == 400 {
		return false
	}
	var failure *upstream.Failure
	if errors.As(err, &failure) {
		if failure.Status == 400 {
			return false
		}
		switch failure.Code {
		case "content_policy_violation", "invalid_image_input", "upstream_text_reply", "unsupported_model", "moderation_blocked", "safety_blocked", "unsupported_image_model":
			return false
		}
	}
	return true
}

func (b *imageExecution) attempt(slot int, lease *accounts.Lease, resume bool, number int, previous string, selectionStarted time.Time) (output Output, err error) {
	ctx := b.record.Trace.BeginAttempt(b.ctx, slot+1, number, lease.Account.ID, lease.Account.Email, previous, lease.Account.Proxy, selectionStarted)
	generatedCount := 0
	attemptConversation := ""
	var postSuccessFailure error
	defer func() {
		defer func() {
			b.mu.Lock()
			b.activeAttempts--
			close(b.released)
			b.released = make(chan struct{})
			b.mu.Unlock()
		}()
		if cause := recover(); cause != nil {
			err = ErrUnavailable
			defer func() { panic(cause) }()
		}
		finish, stop := context.WithTimeout(context.WithoutCancel(b.parent), 5*time.Second)
		defer stop()
		cause := err
		if cause == nil {
			cause = postSuccessFailure
		}
		if e := lease.Finish(finish, err == nil, generatedCount, cause); e != nil {
			err = e
		}
		diagnostic := callrecord.AttemptResult{Status: "success", APIStatusCode: 200, ConversationID: attemptConversation}
		if err != nil {
			diagnostic.Status = "failed"
			if errors.Is(err, context.Canceled) {
				diagnostic.Status = "cancelled"
			}
			diagnostic.ErrorCode = PublicError(err).Code
			diagnostic.PublicError = PublicError(err).Message
			diagnostic.APIStatusCode = PublicError(err).Status
			details := upstream.DiagnosticDetails(err)
			diagnostic.UpstreamError, diagnostic.UpstreamText, diagnostic.RawError = details.UpstreamError, details.UpstreamText, details.RawError
			var f *upstream.Failure
			if errors.As(err, &f) {
				diagnostic.Stage = f.Stage
			}
		}
		b.record.Trace.EndAttempt(ctx, diagnostic)
	}()
	account := lease.Account
	e := func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.record.AccountID = account.ID
		b.record.AccountEmail = account.Email
		b.record.Stage = "image_inputs"
		b.record.Attempts++
		if !resume && b.progress.Slots[slot].Recovery.Valid() {
			// 只有已取得下一账号时才放弃上次只读恢复；先清除再切换账号。
			b.progress.Slots[slot].Recovery = upstream.ImageRecovery{}
			if e := b.checkpoint(slot); e != nil {
				return e
			}
		}
		b.progress.Slots[slot].AccountID = account.ID
		if !resume {
			b.progress.Slots[slot].Recovery = upstream.ImageRecovery{}
		}
		err := b.checkpoint(slot)
		b.notify()
		return err
	}()
	if e != nil {
		return output, e
	}
	var inputs []Input
	if !resume {
		var masks []Input
		inputDone := callrecord.Span(ctx, "image_inputs", "input_ms")
		inputs, masks, e = ReadEditInputs(ctx, b.req.Sources, b.req.MaskSources, account.Proxy)
		if e == nil && len(masks) > 0 {
			if e = b.service.assets.lockOutputDecode(ctx); e == nil {
				func() { defer b.service.assets.decodeMu.Unlock(); inputs, e = CompositeMasks(ctx, inputs, masks) }()
			}
		}
		inputDone(e)
		if e != nil {
			return output, e
		}
	}
	references := make([]upstream.ImageInput, 0, len(inputs))
	dimensions := make([][2]int, 0, len(inputs))
	for _, input := range inputs {
		references = append(references, upstream.ImageInput{Data: input.Data, Name: input.Name, Media: input.Media, Width: input.Width, Height: input.Height})
		dimensions = append(dimensions, [2]int{input.Width, input.Height})
	}
	session, e := b.service.factory(b.policy, account.Proxy)
	if e != nil {
		return output, ErrUnavailable
	}
	defer session.Close()
	recovery := func() upstream.ImageRecovery {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.record.Stage = "image_upstream"
		b.notify()
		return b.progress.Slots[slot].Recovery
	}()
	request := upstream.ImageRequest{Prompt: imagePrompt(b.req), Model: b.req.UpstreamModel, Inputs: references, StreamWindow: time.Duration(b.policy.ImageStreamSeconds) * time.Second, PollWindow: time.Duration(b.policy.ImagePollSeconds) * time.Second, PollInitialWait: b.initialWait, PollInterval: b.interval, SettleEnabled: b.settleEnabled, SettleWindow: b.settleWindow, Validate: func(data []byte) error { _, e := Inspect(data); return e }}
	// Track recovery even for synchronous calls so unknown pending generation cannot be replayed.
	request.Checkpoint = func(r upstream.ImageRecovery) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.progress.Slots[slot].Recovery = r
		return b.checkpoint(slot)
	}
	var generated upstream.ImageResult
	if resume {
		poller, ok := session.(interface {
			PollImage(context.Context, string, upstream.ImageRecovery, upstream.ImageRequest) (upstream.ImageResult, error)
		})
		if !ok {
			return output, ErrUnavailable
		}
		generated, e = poller.PollImage(ctx, account.AccessToken, recovery, request)
	} else if b.req.OutputFormat == "svg" {
		textSession, ok := session.(interface {
			Converse(context.Context, string, []upstream.Message, string, string, func(string) error) (upstream.ConversationResult, error)
		})
		if !ok {
			return output, ErrUnavailable
		}
		var text strings.Builder
		generated.ConversationResult, e = textSession.Converse(ctx, account.AccessToken, []upstream.Message{{Role: "user", Content: strings.TrimSpace(b.req.Prompt) + "\n\n生成格式为 svg。\n请在回复正文中直接包含完整的 SVG XML（从 <svg 开始到 </svg> 结束），不要只返回 sandbox:/mnt/data 下载链接、下载按钮或文字说明；禁止脚本、外部资源和嵌入式位图，必须包含有效的 width/height 或 viewBox。"}}, "auto", "", func(delta string) error {
			if text.Len()+len(delta) > MaxInputBytes {
				return ErrOutput
			}
			text.WriteString(delta)
			return nil
		})
		if e == nil {
			generated.Data = ExtractSVGDocumentText(text.String())
			if generated.Data == nil {
				e = &upstream.Failure{Stage: "image_svg", Code: "upstream_error", Status: 502, UpstreamText: callrecord.CaptureDiagnosticText(text.String()), RawError: "上游文本没有包含完整且安全的 SVG 文档"}
			}
		}
	} else if source, _ := modelConstraints(b.req.Model); source == "codex" {
		codex, ok := session.(interface {
			GenerateCodexImage(context.Context, string, upstream.CodexImageRequest) (upstream.ImageResult, error)
		})
		if !ok {
			return output, ErrUnavailable
		}
		generated, e = codex.GenerateCodexImage(ctx, account.AccessToken, upstream.CodexImageRequest{
			Prompt: b.req.Prompt, Size: b.req.Size, Quality: b.req.Quality, OutputFormat: b.req.OutputFormat,
			Inputs: references, StreamWindow: request.StreamWindow, Validate: request.Validate,
		})
	} else {
		generated, e = session.GenerateImage(ctx, account.AccessToken, request)
	}
	postSuccessFailure = generated.PostSuccessFailure
	attemptConversation = generated.ConversationID
	b.mu.Lock()
	b.record.ConversationID = generated.ConversationID
	b.record.MessageID = generated.MessageID
	b.record.ResolvedModel = generated.ResolvedModel
	if generated.Recovery.Valid() {
		b.progress.Slots[slot].Recovery = generated.Recovery
	}
	recovery = b.progress.Slots[slot].Recovery
	b.mu.Unlock()
	if e != nil {
		var failure *upstream.Failure
		terminal := errors.As(e, &failure) && (failure.Stage == "image_stream" || failure.Stage == "image_tasks")
		if recovery.Valid() && !terminal && !errors.Is(e, upstream.ErrImageText) && !errors.Is(e, upstream.ErrImagePending) {
			return output, errors.Join(upstream.ErrImageSubmissionUnknown, e)
		}
		if terminal || errors.Is(e, upstream.ErrImageText) {
			saved := func() error {
				b.mu.Lock()
				defer b.mu.Unlock()
				b.progress.Slots[slot].Recovery = upstream.ImageRecovery{}
				return b.checkpoint(slot)
			}()
			if saved != nil {
				return output, saved
			}
		}
		return output, e
	}
	b.mu.Lock()
	b.record.Stage = "image_processing"
	b.notify()
	b.mu.Unlock()
	generated.Data, e = b.service.assets.processOutput(ctx, generated.Data, b.req.OutputFormat, b.req.Size, b.upscale)
	if e != nil {
		return output, e
	}
	over := func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.total += len(generated.Data)
		b.record.Stage = "image_publish"
		b.notify()
		return b.total > MaxInputBytes
	}()
	if over {
		return output, ErrOutput
	}
	saveDone := callrecord.Span(ctx, "image_publish", "publish_ms")
	path, e := b.service.assets.Save(ctx, generated.Data)
	saveDone(e)
	if e != nil {
		if errors.Is(e, ErrInput) {
			return output, ErrOutput
		}
		return output, e
	}
	generatedCount = 1
	b.mu.Lock()
	b.published = append(b.published, path)
	b.mu.Unlock()
	info, e := Inspect(generated.Data)
	if e != nil {
		return output, ErrOutput
	}
	output = Output{RevisedPrompt: strings.TrimSpace(b.req.Prompt), Path: strings.TrimPrefix(path, "/images/"), Width: info.Width, Height: info.Height, Bytes: len(generated.Data)}
	if b.req.Format != "b64_json" {
		base := strings.TrimRight(b.req.BaseURL, "/")
		output.URL = base + path
	} else {
		output.B64JSON = base64.StdEncoding.EncodeToString(generated.Data)
	}
	b.mu.Lock()
	b.inputDimensions = dimensions
	if b.task != nil {
		previous := b.progress.clone()
		b.progress.Results = append(b.progress.Results, output)
		b.progress.Slots[slot].Completed = true
		b.progress.Slots[slot].Recovery = upstream.ImageRecovery{}
		if e = b.checkpoint(slot); e != nil {
			b.progress = previous
			b.mu.Unlock()
			return output, e
		}
		b.accepted[path] = true
	}
	b.mu.Unlock()
	if b.removeConversation && generated.ConversationID != "" && b.req.OutputFormat != "svg" {
		if cleaner, ok := session.(interface {
			DeleteConversation(context.Context, string, string) error
		}); ok {
			cleanup, stop := context.WithTimeout(ctx, 10*time.Second)
			done := callrecord.Span(cleanup, "image_cleanup", "cleanup_ms")
			cleanupErr := cleaner.DeleteConversation(cleanup, account.AccessToken, generated.ConversationID)
			done(cleanupErr)
			if cleanupErr != nil {
				details := upstream.DiagnosticDetails(cleanupErr)
				slog.WarnContext(cleanup, "图片已保存，删除官网会话失败", "conversation_id", generated.ConversationID, "error", callrecord.CaptureDiagnosticText(cleanupErr.Error()), "upstream_error", callrecord.CaptureDiagnosticText(details.UpstreamError), "raw_error", callrecord.CaptureDiagnosticText(details.RawError))
			}
			stop()
		}
	}
	return output, nil
}

func imagePrompt(req Request) string {
	prompt := strings.TrimSpace(req.Prompt)
	suffix := ""
	if req.Size != "" {
		suffix += "输出图片尺寸为 " + req.Size + "。"
	}
	if req.Quality != "" {
		suffix += "输出图片质量为 " + req.Quality + "。"
	}
	if req.OutputFormat != "" {
		if suffix != "" {
			suffix += "\n"
		}
		suffix += "生成格式为 " + req.OutputFormat + "。"
	}
	for _, option := range []struct {
		name  string
		value json.RawMessage
	}{{"frame_count", req.FrameCount}, {"frame_duration_ms", req.FrameDurationMS}} {
		if option.value == nil {
			continue
		}
		value := string(option.value)
		var text string
		if json.Unmarshal(option.value, &text) == nil && value != "null" {
			value = text
		}
		if suffix != "" {
			suffix += "\n"
		}
		suffix += option.name + ": " + value
	}
	if suffix != "" {
		prompt += "\n\n" + suffix
	}
	return prompt
}
