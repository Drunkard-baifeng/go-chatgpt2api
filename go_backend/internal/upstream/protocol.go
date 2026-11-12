package upstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"chatgpt2api/go_backend/internal/callrecord"
	"github.com/Drunkard-baifeng/public_golibs/httpclienttls"
	"golang.org/x/net/html"
)

const clientVersion = "prod-a194cd50d4416d3c0b47c740f206b12ce60f5887"
const clientBuild = "6708908"
const defaultPowScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

func uuid() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic("随机源不可用")
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	s := hex.EncodeToString(data[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

// Converse 只执行一次提交；执行者提供覆盖全部准备和读取阶段的总 context。
func (s *Session) Converse(ctx context.Context, token string, messages []Message, model, effort string, emit func(string) error) (ConversationResult, error) {
	var result ConversationResult
	headers, err := s.prepare(ctx, token)
	if err != nil {
		return result, err
	}
	path := "/backend-api/conversation"
	h := headers(path)
	h.Set("Accept", "text/event-stream")
	converted := make([]any, 0, len(messages))
	historyIDs := make(map[string]bool, len(messages))
	for index := range messages {
		message := &messages[index]
		if message.LoadImages != nil {
			message.Images, err = message.LoadImages(ctx, s.proxy)
			if err != nil {
				return result, err
			}
		}
		id := uuid()
		historyIDs[id] = true
		parts := []any{}
		attachments := []any{}
		for _, input := range message.Images {
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
			parts = append(parts, map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://" + meta.FileID, "width": input.Width, "height": input.Height, "size_bytes": len(input.Data)})
			attachments = append(attachments, map[string]any{"id": meta.FileID, "mimeType": input.Media, "name": input.Name, "size": len(input.Data), "width": input.Width, "height": input.Height})
		}
		parts = append(parts, message.Content)
		kind := "text"
		if len(attachments) > 0 {
			kind = "multimodal_text"
		}
		item := map[string]any{"id": id, "author": map[string]any{"role": message.Role}, "content": map[string]any{"content_type": kind, "parts": parts}}
		if len(attachments) > 0 {
			item["metadata"] = map[string]any{"attachments": attachments}
		}
		converted = append(converted, item)
	}
	payload := map[string]any{"action": "next", "messages": converted, "model": model, "parent_message_id": uuid(), "conversation_mode": map[string]any{"kind": "primary_assistant"}, "conversation_origin": nil, "force_paragen": false, "force_paragen_model_slug": "", "force_rate_limit": false, "force_use_sse": true, "history_and_training_disabled": true, "reset_rate_limits": false, "suggestions": []string{}, "supported_encodings": []string{}, "system_hints": []string{}, "timezone": "Asia/Shanghai", "timezone_offset_min": -480, "variant_purpose": "comparison_implicit", "websocket_request_id": uuid(), "client_contextual_info": map[string]any{"is_dark_mode": false, "time_since_loaded": 120, "page_height": 900, "page_width": 1400, "pixel_ratio": 2, "screen_height": 1440, "screen_width": 2560}}
	if effort != "" {
		payload["thinking_effort"] = effort
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, ErrProtocol
	}
	generationDone := callrecord.Span(ctx, "generation_start", "generation_start_ms")
	response, err := s.Do(ctx, http.MethodPost, path, bytes.NewReader(encoded), h)
	if err != nil {
		generationDone(err)
		return result, err
	}
	defer response.Body.Close()
	if err := responseStatus(response, "conversation"); err != nil {
		generationDone(err)
		return result, err
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		err = protocolResponseError(response, "conversation")
		generationDone(err)
		return result, err
	}
	generationDone(nil)
	return readConversation(ctx, response.Body, emit, historyIDs)
}

func safeHeaderToken(s string) bool {
	return len(s) > 0 && len(s) <= 65536 && !strings.ContainsAny(s, "\r\n\x00")
}
func (s *Session) postJSON(ctx context.Context, path string, headers http.Header, payload, target any, stage string) (err error) {
	diagnosticStage := stage
	metric := stage + "_ms"
	switch stage {
	case "image_prepare", "search_prepare", "editable_prepare":
		diagnosticStage, metric = "prepare_conversation", "prepare_conversation_ms"
	case "image_upload_prepare", "editable_upload_prepare":
		metric = "upload_prepare_ms"
	case "image_upload_complete", "editable_upload_complete":
		metric = "upload_complete_ms"
	}
	finish := callrecord.Span(ctx, diagnosticStage, metric)
	defer func() { finish(err) }()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ErrProtocol
	}
	response, err := s.Do(ctx, http.MethodPost, path, bytes.NewReader(encoded), headers)
	if err != nil {
		return err
	}
	data, err := readResponse(response, stage, 1<<20)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, target) != nil {
		return withDiagnostic(ErrProtocol, "", string(data), "", stage+"：上游返回了无效 JSON")
	}
	return nil
}
func readResponse(response *Response, stage string, limit int64) ([]byte, error) {
	defer response.Body.Close()
	if err := responseStatus(response, stage); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}
func checkStatus(status int, stage string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	code := "upstream_http_error"
	switch status {
	case 401:
		code = "upstream_auth_invalid"
	case 403:
		code = "upstream_forbidden"
	case 429:
		code = "upstream_rate_limited"
	case 301, 302, 303, 307, 308:
		code = "upstream_redirect_refused"
	}
	return &Failure{Stage: stage, Code: code, Status: status}
}
func powResources(document []byte) ([]string, string) {
	tokenizer := html.NewTokenizer(bytes.NewReader(document))
	scripts := []string{}
	build := ""
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		for _, attr := range token.Attr {
			if token.Data == "html" && attr.Key == "data-build" && len(attr.Val) < 4096 {
				build = attr.Val
			}
			if token.Data == "script" && attr.Key == "src" && len(attr.Val) < 4096 && len(scripts) < 128 {
				scripts = append(scripts, attr.Val)
			}
		}
	}
	if len(scripts) == 0 {
		scripts = []string{defaultPowScript}
	}
	return scripts, build
}

func (s *Session) prepare(ctx context.Context, token string) (result func(string) http.Header, err error) {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrTarget
	}
	return s.prepareWithHeaders(ctx, s.requestHeaders(token))
}

// requestHeaders 为同一次协议流程保留设备和会话标识，不附加 Sentinel。
func (s *Session) requestHeaders(token string) func(string) http.Header {
	device, session := uuid(), uuid()
	return func(path string) http.Header {
		return http.Header{"Authorization": {"Bearer " + token}, "Origin": {s.origin}, "Referer": {s.origin + "/"}, "Content-Type": {"application/json"}, "Accept": {"application/json"}, "Oai-Device-Id": {device}, "Oai-Session-Id": {session}, "Oai-Language": {oaiLanguage}, "Oai-Client-Version": {clientVersion}, "Oai-Client-Build-Number": {clientBuild}, "X-Openai-Target-Path": {path}, "X-Openai-Target-Route": {path}}
	}
}

func (s *Session) prepareWithHeaders(ctx context.Context, headers func(string) http.Header) (result func(string) http.Header, err error) {
	document, err := func() (document []byte, err error) {
		finish := callrecord.Span(ctx, "bootstrap", "bootstrap_ms")
		defer func() { finish(err) }()
		bootstrap, err := s.do(ctx, http.MethodGet, "/", nil, http.Header{"Accept-Language": {"zh-CN,zh;q=0.9,en;q=0.8"}}, httpclienttls.BrowserModeNavigation)
		if err != nil {
			return nil, err
		}
		return readResponse(bootstrap, "bootstrap", 2<<20)
	}()
	if err != nil {
		return nil, err
	}
	scripts, build := powResources(document)
	requirementsDone := callrecord.Span(ctx, "requirements", "requirements_ms")
	defer func() { requirementsDone(err) }()
	ua := s.client.Profile().UserAgent
	base := "/backend-api/sentinel/chat-requirements"
	p, err := requirementsToken(ua, scripts, build)
	if err != nil {
		return nil, err
	}
	var prepare struct {
		PrepareToken string `json:"prepare_token"`
		Arkose       struct {
			Required bool `json:"required"`
		} `json:"arkose"`
		Proof struct {
			Required   bool   `json:"required"`
			Seed       string `json:"seed"`
			Difficulty string `json:"difficulty"`
		} `json:"proofofwork"`
		Turnstile struct {
			Required bool   `json:"required"`
			DX       string `json:"dx"`
		} `json:"turnstile"`
	}
	if err := s.postJSON(ctx, base+"/prepare", headers(base+"/prepare"), map[string]any{"p": p}, &prepare, "requirements_prepare"); err != nil {
		return nil, err
	}
	if prepare.Arkose.Required {
		return nil, &Failure{Stage: "requirements_prepare", Code: "arkose_required", Status: 502}
	}
	if prepare.PrepareToken == "" || len(prepare.PrepareToken) > 65536 {
		return nil, ErrProtocol
	}
	proof, turnstile := "", ""
	if prepare.Proof.Required {
		proof, err = buildProof(ctx, prepare.Proof.Seed, prepare.Proof.Difficulty, ua, scripts, build)
		if err != nil {
			if err == ErrChallenge {
				return nil, &Failure{Stage: "requirements_proof", Code: "upstream_challenge_failed", Status: 502}
			}
			return nil, err
		}
	}
	if prepare.Turnstile.Required {
		turnstile, err = solveTurnstile(ctx, prepare.Turnstile.DX, p)
		if err != nil {
			return nil, err
		}
	}
	var finalized struct {
		Token string `json:"token"`
		SO    string `json:"so_token"`
	}
	if err := s.postJSON(ctx, base+"/finalize", headers(base+"/finalize"), map[string]any{"prepare_token": prepare.PrepareToken, "proof_token": proof, "turnstile_token": turnstile}, &finalized, "requirements_finalize"); err != nil {
		return nil, err
	}
	if !safeHeaderToken(finalized.Token) || (finalized.SO != "" && !safeHeaderToken(finalized.SO)) {
		return nil, ErrProtocol
	}
	return func(path string) http.Header {
		h := headers(path)
		h.Set("OpenAI-Sentinel-Chat-Requirements-Token", finalized.Token)
		if proof != "" {
			h.Set("OpenAI-Sentinel-Proof-Token", proof)
		}
		if turnstile != "" {
			h.Set("OpenAI-Sentinel-Turnstile-Token", turnstile)
		}
		if finalized.SO != "" {
			h.Set("OpenAI-Sentinel-SO-Token", finalized.SO)
		}
		return h
	}, nil
}
