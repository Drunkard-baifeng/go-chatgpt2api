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
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
)

const EditableModel = "gpt-5-5-thinking"
const EditableEffort = "extended"

const editablePPTPrompt = `我需要你根据用户的需求，来制作一个可以编辑的PPT，你可以使用Agent来做，你不要再继续询问用户问题，内容风格、版式、配色、内容结构和页面信息你可以自行补充并直接执行。整体的流程如下：
1. 用生图的方式，帮我生成一个精美的产品介绍ppt，5-6个页面
2. 帮我把以上涉及到的所有图像和形状素材拆分成单独png，每个素材单独一张图片，不要有遗漏，让我可以直接在ppt里拼接素材还原，不要文字
3. 利用以上所有图片和形状素材，帮我还原你第一次生成的展示ppt，我需要是可编辑的ppt格式，主要部分需要你单独还原插入，文字需要可以编辑
最后只需要给我生成一个PPT文件，以及生成中遇到的各种素材压缩包zip文件就行。`
const editablePSDPrompt = "帮我生成这个图像，把这张海报分成若干图像，包括背景图，每个元素不要改位置，这样子我可以直接在 平时里无需拖动，底色为白色，不要伪透明底。再帮我将以上拆分的图像拼合成一个psd文件，去除白色底，不要改变每个图层的相应位置，保留每个元素所在图层的相应位置，保留每个元素的图层，最后只需要给我输出psd文件，以及每个图层的zip文件"

var ErrEditablePending = errors.New("可编辑文件尚未生成完成，查询预算已用尽")
var editableSandbox = regexp.MustCompile(`(?:sandbox:)?(/mnt/data/[^\s"'\)\]<>]+\.(?i:pptx?|psd|zip))`)
var editableFileID = regexp.MustCompile(`^file[-_][A-Za-z0-9_-]{1,190}$`)

type EditableRequest struct {
	Kind, Prompt               string
	Inputs                     []ImageInput
	StreamWindow, PollInterval time.Duration
	Validate                   func(string, []byte) error
	Progress                   func(ConversationResult) error
}
type EditableArtifact struct {
	Extension string
	Data      []byte
}
type EditableResult struct {
	ConversationResult
	Primary, Archive EditableArtifact
}
type editableRef struct {
	id, name, media, sandbox, message string
}

// GenerateEditable owns one submission only. A known conversation is polled with
// read-only requests; ambiguous failures never trigger a second generation POST.
func (s *Session) GenerateEditable(parent context.Context, token string, req EditableRequest) (result EditableResult, err error) {
	if (req.Kind != "ppt" && req.Kind != "psd") || req.Validate == nil || len(req.Inputs) > 4 || (req.Kind == "psd" && len(req.Inputs) == 0) || len(req.Prompt) > 65536 {
		return result, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(parent, 1200*time.Second)
	defer cancel()
	if req.StreamWindow <= 0 || req.StreamWindow > 300*time.Second {
		req.StreamWindow = 300 * time.Second
	}
	if req.PollInterval <= 0 {
		req.PollInterval = 5 * time.Second
	}
	headers, err := s.prepare(ctx, token)
	if err != nil {
		return result, err
	}
	baseHeaders := headers
	headers = func(p string) http.Header {
		h := baseHeaders(p)
		h.Set("Oai-Client-Version", "prod-bede35f9dcd856d080e012478f0c1031faa2588e")
		h.Set("Oai-Client-Build-Number", "6631702")
		return h
	}
	prompt := editablePPTPrompt
	if req.Kind == "psd" {
		prompt = editablePSDPrompt
	}
	if strings.TrimSpace(req.Prompt) != "" {
		prompt += "\n\n以下是用户补充需求，请直接结合执行：\n" + strings.TrimSpace(req.Prompt)
	}
	parts, attachments, medias := []any{}, []any{}, []string{}
	excluded := map[string]bool{}
	for _, input := range req.Inputs {
		if len(input.Data) == 0 || len(input.Data) > 10<<20 || input.Width < 1 || input.Height < 1 {
			return result, ErrProtocol
		}
		var meta struct {
			FileID    string `json:"file_id"`
			LibraryID string `json:"library_file_id"`
			URL       string `json:"upload_url"`
		}
		p := "/backend-api/files"
		if err = s.postJSON(ctx, p, headers(p), map[string]any{"file_name": input.Name, "file_size": len(input.Data), "use_case": "multimodal", "timezone_offset_min": -480, "reset_rate_limits": false, "store_in_library": true, "library_persistence_mode": "opportunistic"}, &meta, "editable_upload_prepare"); err != nil {
			return result, err
		}
		if !safeAssetID.MatchString(meta.FileID) || (meta.LibraryID != "" && !safeAssetID.MatchString(meta.LibraryID)) {
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
		if err = s.postJSON(ctx, p, headers(p), map[string]any{}, &ack, "editable_upload_complete"); err != nil {
			return result, err
		}
		excluded[meta.FileID] = true
		parts = append(parts, map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "sediment://" + meta.FileID, "width": input.Width, "height": input.Height, "size_bytes": len(input.Data)})
		attachments = append(attachments, map[string]any{"id": meta.FileID, "name": input.Name, "size": len(input.Data), "mime_type": input.Media, "width": input.Width, "height": input.Height, "source": "library", "library_file_id": meta.LibraryID, "is_big_paste": false})
		medias = append(medias, input.Media)
	}
	p := "/backend-api/f/conversation/prepare"
	prepared := struct {
		Conduit string `json:"conduit_token"`
	}{}
	payload := map[string]any{"action": "next", "fork_from_shared_post": false, "parent_message_id": "client-created-root", "model": EditableModel, "client_prepare_state": "success", "timezone_offset_min": -480, "timezone": "Asia/Shanghai", "conversation_mode": map[string]any{"kind": "primary_assistant"}, "system_hints": []string{}, "partial_query": map[string]any{"id": uuid(), "author": map[string]any{"role": "user"}, "content": map[string]any{"content_type": "text", "parts": []string{prompt}}}, "supports_buffering": true, "supported_encodings": []string{"v1"}, "client_contextual_info": map[string]any{"app_name": "chatgpt.com"}, "thinking_effort": EditableEffort}
	if len(medias) > 0 {
		payload["attachment_mime_types"] = medias
	}
	h := headers(p)
	h.Set("X-Conduit-Token", "no-token")
	if err = s.postJSON(ctx, p, h, payload, &prepared, "editable_prepare"); err != nil {
		return result, err
	}
	if !safeHeaderToken(prepared.Conduit) {
		return result, ErrProtocol
	}
	parts = append(parts, prompt)
	contentType := "text"
	if len(req.Inputs) > 0 {
		contentType = "multimodal_text"
	}
	metadata := map[string]any{"developer_mode_connector_ids": []string{}, "selected_sources": []string{}, "selected_github_repos": []string{}, "selected_all_github_repos": false, "system_hints": []string{}, "serialization_metadata": map[string]any{"custom_symbol_offsets": []int{}}}
	if len(attachments) > 0 {
		metadata["attachments"] = attachments
	}
	payload = map[string]any{"action": "next", "messages": []any{map[string]any{"id": uuid(), "author": map[string]any{"role": "user"}, "create_time": float64(time.Now().UnixMilli()) / 1000, "content": map[string]any{"content_type": contentType, "parts": parts}, "metadata": metadata}}, "parent_message_id": "client-created-root", "model": EditableModel, "client_prepare_state": "sent", "timezone_offset_min": -480, "timezone": "Asia/Shanghai", "conversation_mode": map[string]any{"kind": "primary_assistant"}, "enable_message_followups": true, "system_hints": []string{}, "supports_buffering": true, "supported_encodings": []string{"v1"}, "thinking_effort": EditableEffort, "client_contextual_info": map[string]any{"app_name": "chatgpt.com"}, "paragen_cot_summary_display_override": "allow", "force_parallel_switch": "auto"}
	data, _ := json.Marshal(payload)
	p = "/backend-api/f/conversation"
	h = headers(p)
	h.Set("Accept", "text/event-stream")
	h.Set("X-Conduit-Token", prepared.Conduit)
	h.Set("X-Oai-Turn-Trace-Id", uuid())
	stream, stop := context.WithTimeout(ctx, req.StreamWindow)
	generationDone := callrecord.Span(stream, "generation_start", "generation_start_ms")
	response, err := s.Do(stream, "POST", p, bytes.NewReader(data), h)
	if err == nil {
		err = checkStatus(response.StatusCode, "editable_conversation")
		if err == nil {
			media, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if e != nil || media != "text/event-stream" {
				err = ErrProtocol
			}
		}
		generationDone(err)
		if err == nil {
			result.ConversationID, err = readEditableID(stream, response.Body)
		}
		response.Body.Close()
	} else {
		generationDone(err)
	}
	stop()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, err
	}
	if req.Progress != nil {
		if err = req.Progress(result.ConversationResult); err != nil {
			return result, err
		}
	}
	for {
		p = "/backend-api/conversation/" + result.ConversationID
		h = headers(p)
		h.Set("Referer", s.origin+"/c/"+result.ConversationID)
		h.Set("X-Openai-Target-Route", "/backend-api/conversation/{conversation_id}")
		response, err = s.Do(ctx, "GET", p, nil, h)
		if err != nil {
			return result, err
		}
		status := response.StatusCode
		if editableTransient(status) {
			response.Body.Close()
		} else {
			data, err = readResponse(response, "editable_poll", 8<<20)
			if err != nil {
				return result, err
			}
			var document map[string]any
			if json.Unmarshal(data, &document) != nil {
				return result, ErrProtocol
			}
			refs := editableReferences(document, excluded)
			var primary, archive *editableRef
			for i := range refs {
				ref := &refs[i]
				ext := editableExtension(*ref)
				if ext == "zip" {
					archive = ref
				} else if (req.Kind == "ppt" && (ext == "ppt" || ext == "pptx")) || (req.Kind == "psd" && ext == "psd") {
					primary = ref
				}
			}
			if primary != nil && archive != nil {
				if result.Primary, err = s.downloadEditable(ctx, headers, result.ConversationID, *primary, req.Validate); err != nil {
					return result, err
				}
				if result.Archive, err = s.downloadEditable(ctx, headers, result.ConversationID, *archive, req.Validate); err != nil {
					return result, err
				}
				result.MessageID = primary.message
				return result, nil
			}
		}
		if err = waitForPoll(ctx, req.PollInterval, "editable_poll_wait"); err != nil {
			return result, errors.Join(ErrEditablePending, err)
		}
	}
}

func editableTransient(status int) bool {
	switch status {
	case 404, 409, 423, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func readEditableID(ctx context.Context, reader io.Reader) (result string, err error) {
	diagnostics := beginSSEDiagnostics(ctx, "editable")
	defer func() { diagnostics.finish(err) }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	var event strings.Builder
	dispatch := func() (string, error) {
		v := strings.TrimSuffix(event.String(), "\n")
		event.Reset()
		if v == "" {
			return "", nil
		}
		diagnostics.event()
		if v == "[DONE]" {
			return "", ErrProtocol
		}
		var obj any
		if json.Unmarshal([]byte(v), &obj) != nil {
			return "", ErrProtocol
		}
		return editableConversationID(obj, 0), nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			id, err := dispatch()
			if err != nil || id != "" {
				return id, err
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if event.Len()+len(value) > 1<<20 {
				return "", ErrResponseTooLarge
			}
			event.WriteString(value)
			event.WriteByte('\n')
		} else if line == "event: error" {
			return "", &Failure{Stage: "editable_conversation", Code: "upstream_event_error", Status: 502}
		}
	}
	if e := scanner.Err(); e != nil {
		diagnostics.transportError(e)
		return "", safeError(ctx, e)
	}
	if event.Len() > 0 {
		if id, e := dispatch(); e != nil || id != "" {
			return id, e
		}
	}
	return "", ErrProtocol
}
func editableConversationID(obj any, depth int) string {
	if depth > 16 {
		return ""
	}
	switch v := obj.(type) {
	case map[string]any:
		if id, _ := v["conversation_id"].(string); safeAssetID.MatchString(id) {
			return id
		}
		for _, item := range v {
			if id := editableConversationID(item, depth+1); id != "" {
				return id
			}
		}
	case []any:
		for _, item := range v {
			if id := editableConversationID(item, depth+1); id != "" {
				return id
			}
		}
	}
	return ""
}

func editableReferences(document map[string]any, excluded map[string]bool) []editableRef {
	mapping, _ := document["mapping"].(map[string]any)
	current, _ := document["current_node"].(string)
	node, _ := mapping[current].(map[string]any)
	message, _ := node["message"].(map[string]any)
	author, _ := message["author"].(map[string]any)
	channel, _ := message["channel"].(string)
	recipient, _ := message["recipient"].(string)
	// A tool may expose drafts before the turn completes. Confirm the current
	// assistant turn first, then use one message's output set on that branch.
	if current == "" || author["role"] != "assistant" || message["status"] != "finished_successfully" || message["end_turn"] != true || (channel != "" && channel != "final") || (recipient != "" && recipient != "all") {
		return nil
	}
	if refs := editableMessageReferences(message, excluded); len(refs) != 0 {
		return refs
	}
	seen := map[string]bool{current: true}
	for len(seen) <= 2048 {
		parent, _ := node["parent"].(string)
		if parent == "" || seen[parent] {
			return nil
		}
		seen[parent] = true
		node, _ = mapping[parent].(map[string]any)
		message, _ = node["message"].(map[string]any)
		author, _ = message["author"].(map[string]any)
		if author["role"] == "user" || (author["role"] == "assistant" && message["end_turn"] == true) {
			return nil
		}
		if author["role"] == "tool" {
			if message["status"] == "failed" || message["status"] == "error" {
				return nil
			}
			if refs := editableMessageReferences(message, excluded); len(refs) != 0 {
				if message["status"] != "finished_successfully" {
					return nil
				}
				// Never fill a partial new set with an older message's ZIP or deck.
				return refs
			}
		}
	}
	return nil
}

func editableMessageReferences(message map[string]any, excluded map[string]bool) []editableRef {
	id, _ := message["id"].(string)
	if !safeAssetID.MatchString(id) {
		return nil
	}
	refs := []editableRef{}
	var walk func(any, int)
	walk = func(obj any, depth int) {
		if depth > 20 || len(refs) >= 512 {
			return
		}
		switch v := obj.(type) {
		case map[string]any:
			ref := editableRef{message: id}
			for _, key := range []string{"file_id", "id"} {
				value, _ := v[key].(string)
				if editableFileID.MatchString(value) {
					ref.id = value
					break
				}
			}
			if pointer, _ := v["asset_pointer"].(string); ref.id == "" {
				if matches := pointerID.FindStringSubmatch(pointer); len(matches) > 2 {
					ref.id = matches[2]
				}
			}
			for _, key := range []string{"name", "file_name", "filename", "title"} {
				if name, _ := v[key].(string); name != "" {
					ref.name = name
					break
				}
			}
			for _, key := range []string{"mime_type", "mimeType"} {
				if media, _ := v[key].(string); media != "" {
					ref.media = media
					break
				}
			}
			if ref.id != "" && !excluded[ref.id] && editableExtension(ref) != "" {
				refs = append(refs, ref)
			}
			for _, child := range v {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		case string:
			for _, match := range editableSandbox.FindAllStringSubmatch(v, 32) {
				p := match[1]
				if len(p) <= 1024 && path.Clean(p) == p && strings.HasPrefix(p, "/mnt/data/") && !strings.ContainsAny(p, "\\\x00") {
					refs = append(refs, editableRef{name: path.Base(p), sandbox: p, message: id})
				}
			}
		}
	}
	walk(message, 0)
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].id+refs[i].sandbox < refs[j].id+refs[j].sandbox
	})
	return refs
}
func editableExtension(ref editableRef) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(ref.name), "."))
	switch ext {
	case "ppt", "pptx", "psd", "zip":
		return ext
	}
	media, _, _ := mime.ParseMediaType(ref.media)
	switch media {
	case "application/zip", "application/x-zip-compressed":
		return "zip"
	case "image/vnd.adobe.photoshop", "application/vnd.adobe.photoshop":
		return "psd"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "application/vnd.ms-powerpoint":
		return "ppt"
	}
	return ""
}

func (s *Session) downloadEditable(ctx context.Context, headers func(string) http.Header, conversation string, ref editableRef, validate func(string, []byte) error) (EditableArtifact, error) {
	result := EditableArtifact{Extension: editableExtension(ref)}
	paths := []string{}
	if ref.sandbox != "" {
		paths = append(paths, "/backend-api/conversation/"+conversation+"/interpreter/download?"+url.Values{"message_id": {ref.message}, "sandbox_path": {ref.sandbox}}.Encode())
	}
	if ref.id != "" {
		paths = append(paths, "/backend-api/conversation/"+conversation+"/attachment/"+ref.id+"/download", "/backend-api/files/download/"+ref.id+"?post_id=&inline=false", "/backend-api/files/"+ref.id+"/download")
	}
	for _, p := range paths {
		h := headers(p)
		h.Set("Referer", s.origin+"/c/"+conversation)
		response, e := s.Do(ctx, "GET", p, nil, h)
		if e != nil {
			return result, e
		}
		if response.StatusCode == 404 {
			response.Body.Close()
			continue
		}
		data, e := readResponse(response, "editable_resolve", 1<<20)
		if e != nil {
			return result, e
		}
		var value struct {
			Download string `json:"download_url"`
			URL      string `json:"url"`
		}
		if json.Unmarshal(data, &value) != nil {
			return result, ErrProtocol
		}
		raw := value.Download
		if raw == "" {
			raw = value.URL
		}
		if raw == "" {
			continue
		}
		// The transport receives no session headers or Cookie jar.
		result.Data, e = s.transfer(ctx, "GET", raw, s.proxy, nil, "")
		if e != nil {
			return result, e
		}
		if len(result.Data) > assettransfer.MaxBytes {
			return result, ErrResponseTooLarge
		}
		if e = validate(result.Extension, result.Data); e != nil {
			return result, e
		}
		return result, nil
	}
	return result, ErrProtocol
}
