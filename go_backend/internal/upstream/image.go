package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
)

var ErrImageText = errors.New("上游未生成图片，返回了文本或拒绝结果")
var ErrImagePending = errors.New("图片尚未生成完成，查询预算已用尽")
var ErrImageCheckpoint = errors.New("图片会话检查点保存失败")
var ErrImageSubmissionUnknown = errors.New("图片生成提交结果不确定，不能自动重放")

// ImageRecovery 仅供任务所有者私有持久化，不作为公开响应返回。
type ImageRecovery struct {
	ConversationID string   `json:"conversation_id"`
	MessageID      string   `json:"message_id"`
	ResolvedModel  string   `json:"resolved_model"`
	Excluded       []string `json:"excluded"`
}

func (r ImageRecovery) Valid() bool {
	if !safeAssetID.MatchString(r.ConversationID) || (r.MessageID != "" && !safeAssetID.MatchString(r.MessageID)) || len(r.ResolvedModel) > 200 || len(r.Excluded) > MaxImageRecoveryExclusions {
		return false
	}
	for _, id := range r.Excluded {
		if !safeAssetID.MatchString(id) {
			return false
		}
	}
	return true
}

const MaxImageRecoveryExclusions = 32

type ImageInput struct {
	Data          []byte
	Name, Media   string
	Width, Height int
}
type ImageRequest struct {
	Prompt, Model                 string
	Inputs                        []ImageInput
	StreamWindow, PollWindow      time.Duration
	PollInitialWait, PollInterval time.Duration
	SettleEnabled                 bool
	SettleWindow                  time.Duration
	// Validate 由图片领域提供，候选通过完整格式与大小校验后才能选中。
	Validate   func([]byte) error
	Checkpoint func(ImageRecovery) error
}
type ImageResult struct {
	ConversationResult
	Data               []byte
	Recovery           ImageRecovery
	PostSuccessFailure error
}
type imageRef struct{ kind, id string }
type imageState struct {
	ConversationResult
	refs                []imageRef
	excluded            map[string]bool
	message             map[string]any
	terminal, textReply bool
	assistantText       string
	lastDiagnostic      string
	lastConversation    string
	checkpoint          func(ImageRecovery) error
	lastCheckpoint      string
}

func (s *imageState) recovery() ImageRecovery {
	r := ImageRecovery{ConversationID: s.ConversationID, MessageID: s.MessageID, ResolvedModel: s.ResolvedModel, Excluded: []string{}}
	for id := range s.excluded {
		r.Excluded = append(r.Excluded, id)
	}
	sort.Strings(r.Excluded)
	return r
}
func (s *imageState) notify() error {
	if s.checkpoint == nil || s.ConversationID == "" {
		return nil
	}
	r := s.recovery()
	if !r.Valid() {
		return ErrProtocol
	}
	b, _ := json.Marshal(r)
	if string(b) == s.lastCheckpoint {
		return nil
	}
	if err := s.checkpoint(r); err != nil {
		return errors.Join(ErrImageCheckpoint, err)
	}
	s.lastCheckpoint = string(b)
	return nil
}

var safeAssetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
var pointerID = regexp.MustCompile(`(file-service|sediment)://([A-Za-z0-9_-]{1,200})`)
var bareImageID = regexp.MustCompile(`\bfile_00000000[a-f0-9]{24}\b`)

// GenerateImage 只提交一次；已知会话只读恢复，阶段 context 不超过父调用总截止。
func (s *Session) GenerateImage(ctx context.Context, token string, req ImageRequest) (result ImageResult, err error) {
	if req.StreamWindow <= 0 || req.PollWindow <= 0 || req.Model == "" || req.Prompt == "" || req.Validate == nil {
		return result, ErrProtocol
	}
	if !safeHeaderToken(token) {
		return result, ErrTarget
	}
	headers := s.requestHeaders(token)
	state := imageState{excluded: map[string]bool{}, checkpoint: req.Checkpoint}
	submissionUnknown := false
	defer func() {
		result.ConversationResult = state.ConversationResult
		result.Recovery = state.recovery()
		if err != nil && submissionUnknown {
			var failure *Failure
			// HTTP rejection and explicit terminal stream errors are known failures.
			if !errors.As(err, &failure) || (failure.Stage != "image_conversation" && failure.Stage != "image_stream") {
				if !errors.Is(err, ErrImagePending) && !errors.Is(err, ErrImageText) {
					err = errors.Join(ErrImageSubmissionUnknown, err)
				}
			}
		}
	}()
	attachments := []any{}
	parts := []any{}
	for index, input := range req.Inputs {
		// Python 将公共参考图转换为 base64 后，按顺序使用统一附件名称。
		input.Name = "image_" + strconv.Itoa(index+1) + ".png"
		var meta struct {
			FileID string `json:"file_id"`
			URL    string `json:"upload_url"`
		}
		p := "/backend-api/files"
		if err = s.postJSON(ctx, p, headers(p), map[string]any{"file_name": input.Name, "file_size": len(input.Data), "use_case": "multimodal", "width": input.Width, "height": input.Height}, &meta, "image_upload_prepare"); err != nil {
			return result, err
		}
		if !safeAssetID.MatchString(meta.FileID) {
			return result, ErrProtocol
		}
		uploadDone := callrecord.Span(ctx, "upload", "upload_ms")
		_, err = s.transfer(ctx, "PUT", meta.URL, s.proxy, input.Data, input.Media)
		uploadDone(err)
		if err != nil {
			return result, err
		}
		p = "/backend-api/files/" + meta.FileID + "/uploaded"
		var ack map[string]any
		if err = s.postJSON(ctx, p, headers(p), map[string]any{}, &ack, "image_upload_complete"); err != nil {
			return result, err
		}
		state.excluded[meta.FileID] = true
		parts = append(parts, map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://" + meta.FileID, "width": input.Width, "height": input.Height, "size_bytes": len(input.Data)})
		attachments = append(attachments, map[string]any{"id": meta.FileID, "mimeType": input.Media, "name": input.Name, "size": len(input.Data), "width": input.Width, "height": input.Height})
	}
	// Python 先完成参考图上传，再获取供生成会话使用的 Sentinel。
	headers, err = s.prepareWithHeaders(ctx, headers)
	if err != nil {
		return result, err
	}
	path := "/backend-api/f/conversation/prepare"
	var prepared struct {
		Conduit string `json:"conduit_token"`
	}
	hints := []string{"picture_v2"}
	payload := map[string]any{"action": "next", "fork_from_shared_post": false, "parent_message_id": uuid(), "model": req.Model, "client_prepare_state": "success", "timezone_offset_min": -480, "timezone": "Asia/Shanghai", "conversation_mode": map[string]any{"kind": "primary_assistant"}, "system_hints": hints, "partial_query": map[string]any{"id": uuid(), "author": map[string]any{"role": "user"}, "content": map[string]any{"content_type": "text", "parts": []string{req.Prompt}}}, "supports_buffering": true, "supported_encodings": []string{"v1"}, "client_contextual_info": map[string]any{"app_name": "chatgpt.com"}}
	if err = s.postJSON(ctx, path, headers(path), payload, &prepared, "image_prepare"); err != nil {
		return result, err
	}
	if !safeHeaderToken(prepared.Conduit) {
		return result, ErrProtocol
	}
	parts = append(parts, req.Prompt)
	kind := "text"
	if len(req.Inputs) > 0 {
		kind = "multimodal_text"
	}
	metadata := map[string]any{"developer_mode_connector_ids": []string{}, "selected_github_repos": []string{}, "selected_all_github_repos": false, "system_hints": hints, "serialization_metadata": map[string]any{"custom_symbol_offsets": []int{}}}
	if len(attachments) > 0 {
		metadata["attachments"] = attachments
	}
	inputID := uuid()
	state.excluded[inputID] = true
	payload = map[string]any{"action": "next", "messages": []any{map[string]any{"id": inputID, "author": map[string]any{"role": "user"}, "create_time": float64(time.Now().UnixMilli()) / 1000, "content": map[string]any{"content_type": kind, "parts": parts}, "metadata": metadata}}, "parent_message_id": uuid(), "model": req.Model, "client_prepare_state": "sent", "timezone_offset_min": -480, "timezone": "Asia/Shanghai", "conversation_mode": map[string]any{"kind": "primary_assistant"}, "enable_message_followups": true, "system_hints": hints, "supports_buffering": true, "supported_encodings": []string{"v1"}, "client_contextual_info": map[string]any{"app_name": "chatgpt.com", "is_dark_mode": false, "time_since_loaded": 1200, "page_height": 1072, "page_width": 1724, "pixel_ratio": 1.2, "screen_height": 1440, "screen_width": 2560}, "paragen_cot_summary_display_override": "allow", "force_parallel_switch": "auto"}
	encoded, _ := json.Marshal(payload)
	path = "/backend-api/f/conversation"
	h := headers(path)
	h.Set("Accept", "text/event-stream")
	h.Set("X-Conduit-Token", prepared.Conduit)
	h.Set("X-Oai-Turn-Trace-Id", uuid())
	stream, cancel := context.WithTimeout(ctx, req.StreamWindow)
	submissionUnknown = true
	generationDone := callrecord.Span(stream, "generation_start", "generation_start_ms")
	response, err := s.Do(stream, "POST", path, bytes.NewReader(encoded), h)
	if err == nil {
		err = responseStatus(response, "image_conversation")
		if err == nil {
			media, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if e != nil || media != "text/event-stream" {
				err = protocolResponseError(response, "image_conversation")
			}
		}
		generationDone(err)
		if err == nil {
			err = state.read(stream, response.Body)
		}
		response.Body.Close()
	} else {
		generationDone(err)
	}
	cancel()
	result.ConversationResult = state.ConversationResult
	if ctx.Err() != nil {
		return result, errors.Join(ctx.Err(), err)
	}
	var failure *Failure
	if errors.As(err, &failure) || errors.Is(err, ErrProtocol) || errors.Is(err, ErrResponseTooLarge) || errors.Is(err, ErrImageCheckpoint) {
		return result, err
	}
	if state.ConversationID == "" {
		if err != nil {
			return result, err
		}
		return result, ErrProtocol
	}
	if len(state.refs) == 0 && state.terminal && state.textReply {
		return result, state.textError()
	}
	return s.pollImage(ctx, headers, &state, req)
}

// PollImage 只读已知会话；不上传、不 prepare conversation、不提交生成。
func (s *Session) PollImage(ctx context.Context, token string, recovery ImageRecovery, req ImageRequest) (result ImageResult, err error) {
	if !recovery.Valid() || req.PollWindow <= 0 || req.Validate == nil {
		return result, ErrProtocol
	}
	headers, err := s.prepare(ctx, token)
	if err != nil {
		return result, err
	}
	state := imageState{ConversationResult: ConversationResult{ConversationID: recovery.ConversationID, MessageID: recovery.MessageID, ResolvedModel: recovery.ResolvedModel}, excluded: map[string]bool{}, checkpoint: req.Checkpoint}
	for _, id := range recovery.Excluded {
		state.excluded[id] = true
	}
	return s.pollImage(ctx, headers, &state, req)
}

func (s *Session) pollImage(ctx context.Context, headers func(string) http.Header, state *imageState, req ImageRequest) (result ImageResult, err error) {
	defer func() {
		result.ConversationResult = state.ConversationResult
		result.Recovery = state.recovery()
		if err != nil {
			details := DiagnosticDetails(err)
			raw := details.RawError
			if raw == "" {
				raw = state.lastDiagnostic
			}
			err = withDiagnostic(err, "", "", state.assistantText, raw)
		}
	}()
	poll, stop := context.WithTimeout(ctx, req.PollWindow)
	defer stop()
	pending := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrImagePending
	}
	if len(state.refs) == 0 && req.PollInitialWait > 0 {
		if waitForPoll(poll, req.PollInitialWait, "image_poll_wait") != nil {
			return result, pending()
		}
	}
	stableKey := ""
	var stableAt time.Time
	confirmed := !req.SettleEnabled || req.SettleWindow <= 0
	var lastFailure error
	cycles, retries := 0, 0
	for {
		if poll.Err() != nil {
			// Python 在结果探测超时后仍解析已知产物，生成总截止保持不变。
			if ctx.Err() == nil && len(state.refs) > 0 {
				data, e := s.resolveImageCandidates(ctx, headers, state, req)
				if e == nil {
					result.Data = data
					return result, nil
				}
				return result, e
			}
			if ctx.Err() == nil && lastFailure != nil {
				return result, lastFailure
			}
			return result, pending()
		}
		resolveMiss := false
		if len(state.refs) > 0 && confirmed {
			data, resolveErr := s.resolveImageCandidates(poll, headers, state, req)
			if resolveErr == nil {
				result.Data = data
				return result, nil
			}
			var f *Failure
			if errors.As(resolveErr, &f) && f.Status != 404 && f.Status != 429 && f.Status < 500 {
				return result, resolveErr
			}
			if errors.Is(resolveErr, ErrResponseTooLarge) {
				return result, resolveErr
			}
			lastFailure = resolveErr
			resolveMiss = true
		}
		if len(state.refs) > 0 && !confirmed {
			key := imageReferenceKey(state.refs)
			if key != stableKey {
				stableKey = key
				stableAt = time.Now().Add(req.SettleWindow)
			}
			if remaining := time.Until(stableAt); remaining > 0 {
				if waitForPoll(poll, remaining, "image_settle_wait") != nil {
					continue
				}
			}
		}
		cycles++
		// 与 Python 一样先探测任务，再读会话；任务错误不能覆盖实际图片产物。
		taskErr := s.imageTaskFailure(poll, headers, state.ConversationID)
		p := "/backend-api/conversation/" + state.ConversationID
		documentErr := func() (requestErr error) {
			done := callrecord.Span(poll, "image_poll_request", "poll_request_ms")
			defer func() { done(requestErr) }()
			response, e := s.Do(poll, "GET", p, nil, headers(p))
			if e != nil {
				return e
			}
			body, e := readResponse(response, "image_poll", 1<<20)
			if e != nil {
				return e
			}
			var doc map[string]any
			if json.Unmarshal(body, &doc) != nil {
				return withDiagnostic(ErrProtocol, "", string(body), "", "image_poll：会话响应不是有效 JSON")
			}
			e = state.document(doc)
			if e != nil {
				return withDiagnostic(e, "", diagnosticJSON(doc), "", "image_poll：解析会话结果失败")
			}
			return state.notify()
		}()
		state.lastDiagnostic = "图片结果轮询：conversation_id=" + state.ConversationID + "，轮数=" + strconv.Itoa(cycles) + "，图片引用=" + strconv.Itoa(len(state.refs))
		if state.lastConversation != "" {
			state.lastDiagnostic += "\n最后会话快照：" + state.lastConversation
		}
		if len(state.refs) > 0 {
			// 已有可用图片时，任务探测失败只作诊断，不把成功改为失败。
			if documentErr != nil && !errors.Is(documentErr, ErrImageCheckpoint) {
				confirmed = true
				continue
			}
			if req.SettleEnabled && req.SettleWindow > 0 {
				key := imageReferenceKey(state.refs)
				confirmed = key == stableKey && !stableAt.IsZero() && !time.Now().Before(stableAt) && documentErr == nil
				if key != stableKey {
					stableKey = key
					stableAt = time.Now().Add(req.SettleWindow)
				}
			}
			if documentErr == nil && !resolveMiss {
				lastFailure = nil
				continue
			}
		}
		if documentErr == nil && len(state.refs) == 0 && state.terminal && state.textReply {
			// 可读正文优先于只有 failed 标志的泛化任务错误。
			var f *Failure
			if taskErr == nil || !errors.As(taskErr, &f) || f.Code == "upstream_error" {
				return result, state.textError()
			}
		}
		if taskErr != nil {
			var f *Failure
			if errors.As(taskErr, &f) {
				if f.Status == 401 || f.Status == 403 || (f.Stage == "image_stream" && f.Code != "upstream_error") || f.Code == "image_tool_error" {
					return result, taskErr
				}
				if f.Code == "upstream_error" {
					lastFailure = taskErr
				}
			}
		}
		if documentErr != nil {
			if errors.Is(documentErr, ErrProtocol) || errors.Is(documentErr, ErrResponseTooLarge) || errors.Is(documentErr, ErrImageCheckpoint) {
				return result, documentErr
			}
			var f *Failure
			if errors.As(documentErr, &f) && f.Status != 404 && f.Status != 429 && f.Status < 500 {
				return result, documentErr
			}
			// 轮询窗口到期只是阶段预算用尽，不能把它保存为传输错误，
			// 否则下面会将已受理的会话误报为总超时及提交状态不确定。
			if poll.Err() == nil || !errors.Is(documentErr, poll.Err()) {
				lastFailure = documentErr
			}
		}
		interval := req.PollInterval
		if interval <= 0 {
			interval = 3 * time.Second
		}
		if documentErr != nil {
			retries++
			interval = time.Duration(1<<min(retries, 4)) * time.Second
			var f *Failure
			if errors.As(documentErr, &f) && f.HasRetryAfter {
				interval = f.RetryAfter
			}
		} else {
			retries = 0
		}
		if waitForPoll(poll, interval, "image_poll_wait") != nil {
			continue
		}
	}
}

func imageReferenceKey(refs []imageRef) string {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		values = append(values, ref.kind+":"+ref.id)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

// 每次只解析首个完整有效产物；不得因多候选改变公开 n。
func (s *Session) resolveImageCandidates(ctx context.Context, headers func(string) http.Header, state *imageState, req ImageRequest) ([]byte, error) {
	var last error
	for _, ref := range state.refs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := "/backend-api/files/" + ref.id + "/download"
		if ref.kind == "sediment" {
			p = "/backend-api/conversation/" + state.ConversationID + "/attachment/" + ref.id + "/download"
		}
		var signed struct {
			Download string `json:"download_url"`
			URL      string `json:"url"`
		}
		e := func() (err error) {
			done := callrecord.Span(ctx, "image_resolve", "resolve_ms")
			defer func() { done(err) }()
			response, err := s.Do(ctx, "GET", p, nil, headers(p))
			if err != nil {
				return err
			}
			body, err := readResponse(response, "image_resolve", 1<<20)
			if err != nil {
				return err
			}
			if json.Unmarshal(body, &signed) != nil {
				return withDiagnostic(ErrProtocol, "", string(body), "", "image_resolve：下载地址响应不是有效 JSON")
			}
			return nil
		}()
		if e == nil {
			raw := signed.Download
			if raw == "" {
				raw = signed.URL
			}
			var data []byte
			data, e = s.downloadImage(ctx, headers, raw)
			if e == nil {
				e = req.Validate(data)
				if e == nil {
					return data, nil
				}
				e = withDiagnostic(ErrProtocol, "", "", "", "上游下载的候选图片未通过完整格式校验")
			}
		}
		var f *Failure
		if errors.As(e, &f) && f.Status != 404 && f.Status != 429 && f.Status < 500 {
			return nil, e
		}
		if errors.Is(e, ErrResponseTooLarge) {
			return nil, e
		}
		last = e
	}
	if last == nil {
		last = ErrImagePending
	}
	return nil, last
}

func (s *Session) imageTaskFailure(parent context.Context, headers func(string) http.Header, conversation string) (err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	done := callrecord.Span(ctx, "image_poll_tasks_request", "poll_request_ms")
	defer func() { done(err) }()
	p := "/backend-api/tasks"
	response, err := s.Do(ctx, "GET", p, nil, headers(p))
	if err != nil {
		return err
	}
	body, err := readResponse(response, "image_tasks", 1<<20)
	if err != nil {
		return err
	}
	var doc struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return withDiagnostic(ErrProtocol, "", string(body), "", "image_tasks：任务响应不是有效 JSON")
	}
	var found error
	for _, task := range doc.Tasks {
		if task["conversation_id"] != conversation && task["original_conversation_id"] != conversation {
			continue
		}
		message, ok := task["image_gen_message"].(map[string]any)
		if !ok {
			continue
		}
		failure := imageStreamFailure(message, 0)
		if failure == nil {
			metadata, _ := message["metadata"].(map[string]any)
			content, _ := message["content"].(map[string]any)
			if message["status"] == "failed" || message["status"] == "error" || metadata["is_error"] == true || content["content_type"] == "system_error" {
				code := "upstream_error"
				if content["content_type"] == "system_error" {
					code = "image_tool_error"
				}
				failure = imageFailureDetail(message, &Failure{Stage: "image_tasks", Code: code, Status: 502})
			}
		}
		found = preferImageFailure(found, failure)
	}
	return found
}

func preferImageFailure(current, candidate error) error {
	if candidate == nil {
		return current
	}
	if current == nil {
		return candidate
	}
	priority := func(err error) int {
		var f *Failure
		if !errors.As(err, &f) {
			return 0
		}
		switch {
		case f.Status == 401 || f.Status == 403:
			return 8
		case f.Code == "image_quota_exhausted":
			return 7
		case f.Status == 429:
			return 6
		case f.Code == "content_policy_violation":
			return 4
		case f.Code == "image_tool_error":
			return 3
		case f.Status == 400:
			return 2
		}
		return 0
	}
	if priority(candidate) >= priority(current) {
		return candidate
	}
	return current
}

// downloadImage 与 Python 一样区分站内受保护图片和外站签名资源。
// 账号头和 Cookie 仅用于当前固定上游站点；站内响应不跟随重定向。
func (s *Session) downloadImage(parent context.Context, headers func(string) http.Header, raw string) (result []byte, err error) {
	finish := callrecord.Span(parent, "image_download", "download_ms")
	defer func() { finish(err) }()
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 16384 || u.User != nil || u.Fragment != "" {
		return nil, assettransfer.ErrTransfer
	}
	origin, err := url.Parse(s.origin)
	if err != nil {
		return nil, ErrTarget
	}
	sameOrigin := u.Scheme == "" && u.Host == "" && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//")
	sameOrigin = sameOrigin || (u.Scheme == origin.Scheme && strings.EqualFold(u.Host, origin.Host))
	if !sameOrigin {
		return s.transfer(parent, "GET", raw, s.proxy, nil, "")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	path := u.RequestURI()
	h := headers(u.EscapedPath())
	h.Set("Accept", "image/*")
	response, err := s.Do(ctx, "GET", path, nil, h)
	if err != nil {
		return nil, err
	}
	return readResponse(response, "image_download", assettransfer.MaxBytes)
}

func (s *imageState) read(ctx context.Context, r io.Reader) (err error) {
	diagnostics := beginSSEDiagnostics(ctx, "image")
	defer func() { diagnostics.finish(err) }()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	var event strings.Builder
	eventType := ""
	dispatch := func() error {
		data := strings.TrimSpace(event.String())
		event.Reset()
		failureEvent := eventType == "error"
		eventType = ""
		if data != "" {
			diagnostics.event()
		}
		if failureEvent && (data == "" || data == "[DONE]") {
			return &Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502}
		}
		if data == "[DONE]" {
			return io.EOF
		}
		if data == "" {
			return nil
		}
		var decoded any
		if json.Unmarshal([]byte(data), &decoded) != nil {
			return withDiagnostic(ErrProtocol, "", data, "", "image_stream：SSE data 不是有效 JSON")
		}
		// v1 图片流以 delta_encoding 的字符串标记开场。与 Python
		// 一样忽略非对象元数据，再处理后续消息和图片引用。
		v, ok := decoded.(map[string]any)
		if !ok {
			if failureEvent {
				return withDiagnostic(&Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502}, failureMessage(decoded, 0), data, "", "")
			}
			return nil
		}
		if err := s.event(v, 0); err != nil {
			return withDiagnostic(err, "", diagnosticJSON(v), "", "image_stream：处理 SSE 事件失败")
		}
		if failureEvent {
			return imageFailureDetail(v, &Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502})
		}
		if err := s.notify(); err != nil {
			return err
		}
		// 已确认面向用户的结束回复无需等待 HTTP 关闭；工具参数和
		// end_turn=false 的进度仍继续读取，已取得的图片引用继续保留。
		if s.terminal && s.textReply {
			return io.EOF
		}
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		if line == "" {
			if e := dispatch(); e != nil {
				if e == io.EOF {
					return nil
				}
				return e
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if event.Len()+len(value) > 1<<20 {
				return ErrResponseTooLarge
			}
			event.WriteString(value)
			event.WriteByte('\n')
		} else if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if scanner.Err() != nil {
		diagnostics.transportError(scanner.Err())
		return safeError(ctx, scanner.Err())
	}
	if event.Len() > 0 {
		if e := dispatch(); e != nil && e != io.EOF {
			return e
		}
	}
	return nil
}
func (s *imageState) event(v map[string]any, depth int) error {
	if depth > 16 {
		return ErrProtocol
	}
	if id, ok := v["conversation_id"].(string); ok {
		if !safeAssetID.MatchString(id) {
			return ErrProtocol
		}
		s.ConversationID = id
		if err := s.notify(); err != nil {
			return err
		}
	}
	if err := imageStreamFailure(v, 0); err != nil {
		return err
	}
	if e, ok := v["error"]; (ok && e != nil && e != false) || v["type"] == "error" || v["type"] == "response.failed" || v["type"] == "response.incomplete" {
		return imageFailureDetail(v, &Failure{Stage: "image_stream", Code: "upstream_event_error", Status: 502})
	}
	if m, ok := v["message"].(map[string]any); ok {
		s.message = m
		return s.inspect()
	}
	if m, ok := v["v"].(map[string]any); ok {
		if m["message"] != nil || m["conversation_id"] != nil {
			return s.event(m, depth+1)
		}
	}
	op, _ := v["o"].(string)
	p, _ := v["p"].(string)
	if op == "patch" {
		items, ok := v["v"].([]any)
		if !ok {
			return ErrProtocol
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return ErrProtocol
			}
			if err := s.event(m, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if p == "/message" && (op == "add" || op == "replace") {
		m, ok := v["v"].(map[string]any)
		if !ok {
			return ErrProtocol
		}
		s.message = m
		return s.inspect()
	}
	if strings.HasPrefix(p, "/message/") && s.message != nil {
		if _, hasOperation := v["o"]; !hasOperation && (p == "/message/metadata" || strings.HasPrefix(p, "/message/metadata/")) {
			// Python 按字段提取 metadata 事实，不要求 p/v 更新携带 o。
			// 仅为此类元数据赋值；正文和图片补丁仍要求显式操作。
			op = "replace"
			if p != "/message/metadata" && s.message["metadata"] == nil {
				s.message["metadata"] = map[string]any{}
			}
		}
		_, err := patchImage(s.message, strings.Split(strings.TrimPrefix(p, "/message/"), "/"), op, v["v"], 0)
		if err != nil {
			return err
		}
		encoded, e := json.Marshal(s.message)
		if e != nil || len(encoded) > 1<<20 {
			return ErrResponseTooLarge
		}
		return s.inspect()
	}
	return nil
}
func patchImage(node any, path []string, op string, value any, depth int) (any, error) {
	if depth > 20 {
		return nil, ErrProtocol
	}
	if len(path) == 0 {
		switch op {
		case "replace", "add":
			return value, nil
		case "append":
			switch n := node.(type) {
			case map[string]any:
				v, ok := value.(map[string]any)
				if !ok {
					return nil, ErrProtocol
				}
				for key, item := range v {
					n[key] = item
				}
				return n, nil
			case string:
				v, ok := value.(string)
				if !ok || len(n)+len(v) > 1<<20 {
					return nil, ErrProtocol
				}
				return n + v, nil
			case []any:
				v, ok := value.([]any)
				if !ok || len(n)+len(v) > 256 {
					return nil, ErrProtocol
				}
				return append(n, v...), nil
			}
		}
		return nil, ErrProtocol
	}
	key := strings.ReplaceAll(strings.ReplaceAll(path[0], "~1", "/"), "~0", "~")
	switch n := node.(type) {
	case map[string]any:
		if len(path) == 1 && op == "remove" {
			delete(n, key)
			return n, nil
		}
		updated, err := patchImage(n[key], path[1:], op, value, depth+1)
		if err != nil {
			return nil, err
		}
		n[key] = updated
		return n, nil
	case []any:
		if len(path) == 1 && op == "add" {
			i, e := strconv.Atoi(key)
			if key == "-" {
				i = len(n)
				e = nil
			}
			if e != nil || i < 0 || i > len(n) || len(n) >= 256 {
				return nil, ErrProtocol
			}
			n = append(n, nil)
			copy(n[i+1:], n[i:])
			n[i] = value
			return n, nil
		}
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(n) {
			return nil, ErrProtocol
		}
		if len(path) == 1 && op == "remove" {
			return append(n[:i], n[i+1:]...), nil
		}
		updated, err := patchImage(n[i], path[1:], op, value, depth+1)
		n[i] = updated
		return n, err
	}
	return nil, ErrProtocol
}
func (s *imageState) inspect() error {
	m := s.message
	author, _ := m["author"].(map[string]any)
	role, _ := author["role"].(string)
	role = strings.ToLower(strings.TrimSpace(role))
	id, _ := m["id"].(string)
	channel, _ := m["channel"].(string)
	channel = strings.ToLower(strings.TrimSpace(channel))
	s.terminal, s.textReply = false, false
	if s.excluded[id] || (role != "tool" && role != "assistant") || channel == "analysis" {
		return nil
	}
	if len(id) > 255 {
		return ErrProtocol
	}
	s.MessageID = id
	metadata, _ := m["metadata"].(map[string]any)
	if model, ok := metadata["model_slug"].(string); ok && len(model) <= 255 {
		s.ResolvedModel = model
	}
	collect := func(pointer string) error {
		candidates := []imageRef{}
		for _, match := range pointerID.FindAllStringSubmatch(pointer, -1) {
			candidates = append(candidates, imageRef{match[1], match[2]})
		}
		for _, id := range bareImageID.FindAllString(pointer, -1) {
			candidates = append(candidates, imageRef{"file-service", id})
		}
		for _, ref := range candidates {
			found := s.excluded[ref.id]
			for _, old := range s.refs {
				found = found || old == ref
			}
			if !found {
				if len(s.refs) >= 16 {
					return ErrResponseTooLarge
				}
				s.refs = append(s.refs, ref)
			}
		}
		return nil
	}
	var walk func(any, int) error
	walk = func(v any, d int) error {
		if d > 24 {
			return ErrProtocol
		}
		switch n := v.(type) {
		case map[string]any:
			if pointer, ok := n["asset_pointer"].(string); ok {
				if err := collect(pointer); err != nil {
					return err
				}
			}
			keys := make([]string, 0, len(n))
			for key := range n {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				item := n[key]
				if err := walk(item, d+1); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range n {
				if err := walk(item, d+1); err != nil {
					return err
				}
			}
		case string:
			if role == "tool" {
				return collect(n)
			}
		}
		return nil
	}
	if err := walk(m["content"], 0); err != nil {
		return err
	}
	if err := walk(metadata, 0); err != nil {
		return err
	}
	if err := imageStreamFailure(m, 0); err != nil {
		return err
	}
	status, _ := m["status"].(string)
	if strings.TrimSpace(status) == "" {
		status, _ = metadata["status"].(string)
	}
	status = strings.ToLower(strings.TrimSpace(status))
	recipient, _ := m["recipient"].(string)
	if role == "assistant" && (channel == "" || channel == "final" || channel == "commentary") && (recipient == "" || recipient == "all") {
		content, _ := m["content"].(map[string]any)
		if content["content_type"] == "text" || content["content_type"] == "code" {
			text := imageMessageText(content)
			s.textReply = text != ""
			if end, exists := m["end_turn"]; end == true || (!exists && imageTerminalStatus(status)) {
				s.terminal = true
				s.assistantText = text
				// 与 Python 一样，带 is_error 的已结束 assistant 文本
				// 仍是文本结果；不从审核说明的文字推算错误码。
				if s.textReply {
					return nil
				}
			}
		}
	}
	if status == "failed" || status == "error" {
		return imageFailureDetail(m, &Failure{Stage: "image_stream", Code: "upstream_error", Status: 502})
	}
	return nil
}

func imageTerminalStatus(status string) bool {
	switch status {
	case "complete", "completed", "done", "finished", "finished_successfully", "finished_partial_completion", "success", "succeeded":
		return true
	}
	return false
}

func imageMessageText(content map[string]any) string {
	var text strings.Builder
	parts, _ := content["parts"].([]any)
	for _, part := range parts {
		if value, ok := part.(string); ok {
			text.WriteString(value)
		}
	}
	if value := strings.TrimSpace(text.String()); value != "" {
		return value
	}
	value, _ := content["text"].(string)
	return strings.TrimSpace(value)
}
func (s *imageState) document(doc map[string]any) error {
	mapping, ok := doc["mapping"].(map[string]any)
	if !ok {
		return ErrProtocol
	}
	if len(mapping) > 2048 {
		return ErrResponseTooLarge
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	created := func(k string) float64 {
		node, _ := mapping[k].(map[string]any)
		m, _ := node["message"].(map[string]any)
		v, _ := m["create_time"].(float64)
		return v
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := created(keys[i]), created(keys[j])
		if a == b {
			return keys[i] < keys[j]
		}
		return a < b
	})
	// 当前分支沿 current_node 回溯；缺少分支信息时才按时间排序。
	if current, ok := doc["current_node"].(string); ok && mapping[current] != nil {
		keys = keys[:0]
		visited := map[string]bool{}
		for current != "" && !visited[current] {
			visited[current] = true
			node, ok := mapping[current].(map[string]any)
			if !ok {
				break
			}
			keys = append(keys, current)
			current, _ = node["parent"].(string)
		}
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}
	lastUser := -1
	for i, key := range keys {
		node, _ := mapping[key].(map[string]any)
		message, _ := node["message"].(map[string]any)
		author, _ := message["author"].(map[string]any)
		if author["role"] == "user" {
			lastUser = i
		}
	}
	keys = keys[lastUser+1:]
	// 只保留当前轮最近 8 条消息的有界诊断，避免真实等待超时只剩轮数。
	// 不保存用户原文、请求头、完整会话或任意 metadata。
	snapshot := []any{}
	for _, key := range keys[max(0, len(keys)-8):] {
		node, _ := mapping[key].(map[string]any)
		message, _ := node["message"].(map[string]any)
		if message == nil {
			continue
		}
		author, _ := message["author"].(map[string]any)
		content, _ := message["content"].(map[string]any)
		metadata, _ := message["metadata"].(map[string]any)
		text := []rune(imageMessageText(content))
		text = text[:min(len(text), 500)]
		snapshot = append(snapshot, map[string]any{"message_id": key, "role": author["role"], "channel": message["channel"], "status": message["status"], "end_turn": message["end_turn"], "content_type": content["content_type"], "is_error": metadata["is_error"], "async_task_type": metadata["async_task_type"], "text_preview": string(text)})
	}
	s.lastConversation = diagnosticJSON(map[string]any{"mapping_count": len(mapping), "messages": snapshot})
	s.terminal, s.textReply, s.assistantText = false, false, ""
	var failure error
	terminalText := ""
	for _, key := range keys {
		raw := mapping[key]
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		m, ok := node["message"].(map[string]any)
		if ok {
			s.message = m
			if err := s.inspect(); err != nil {
				if errors.Is(err, ErrProtocol) || errors.Is(err, ErrResponseTooLarge) {
					return err
				}
				failure = preferImageFailure(failure, err)
			}
			if s.terminal && s.assistantText != "" {
				terminalText = s.assistantText
			}
		}
	}
	if terminalText != "" {
		s.terminal, s.textReply, s.assistantText = true, true, terminalText
	}
	if len(s.refs) > 0 {
		return nil
	}
	var f *Failure
	if terminalText != "" && errors.As(failure, &f) && f.Code == "upstream_error" {
		return nil
	}
	return failure
}
