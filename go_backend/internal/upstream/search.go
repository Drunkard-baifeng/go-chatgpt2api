package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
)

type SearchSource struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet"`
	SourceType string `json:"source_type"`
}
type SearchImageGroup struct {
	Queries     []string `json:"queries"`
	AspectRatio string   `json:"aspect_ratio,omitempty"`
	NumPerQuery int      `json:"num_per_query,omitempty"`
}
type SearchResult struct {
	ConversationID     string             `json:"conversation_id"`
	Status             string             `json:"status"`
	Answer             string             `json:"answer"`
	Sources            []SearchSource     `json:"sources"`
	ImageGroups        []SearchImageGroup `json:"image_groups"`
	AssistantMessageID string             `json:"assistant_message_id"`
	CreateTime         float64            `json:"create_time"`
}

// Search 只提交一次，后续只读取同一会话，所有阶段服从调用者总截止。
func (s *Session) Search(ctx context.Context, token, prompt string) (SearchResult, error) {
	var result SearchResult
	if token == "" || strings.ContainsAny(token, "\r\n\x00") || strings.TrimSpace(prompt) == "" || len(prompt) > 1<<20 {
		return result, ErrProtocol
	}
	baseHeaders := http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}, "Accept": {"application/json"}, "Origin": {s.origin}, "Referer": {s.origin + "/"}, "X-Conduit-Token": {"no-token"}}
	path := "/backend-api/f/conversation/prepare"
	base := map[string]any{"action": "next", "fork_from_shared_post": false, "parent_message_id": "client-created-root", "model": "gpt-5-5", "client_prepare_state": "success", "timezone_offset_min": -480, "timezone": "Asia/Shanghai", "conversation_mode": map[string]any{"kind": "primary_assistant"}, "system_hints": []string{"search"}, "supports_buffering": true, "supported_encodings": []string{"v1"}, "client_contextual_info": map[string]any{"app_name": "chatgpt.com"}}
	base["partial_query"] = map[string]any{"id": uuid(), "author": map[string]any{"role": "user"}, "content": map[string]any{"content_type": "text", "parts": []string{prompt}}}
	var prepared struct {
		Token string `json:"conduit_token"`
	}
	if err := s.postJSON(ctx, path, baseHeaders, base, &prepared, "search_prepare"); err != nil {
		return result, err
	}
	if !safeHeaderToken(prepared.Token) {
		return result, ErrProtocol
	}
	headers, err := s.prepare(ctx, token)
	if err != nil {
		return result, err
	}
	delete(base, "partial_query")
	delete(base, "fork_from_shared_post")
	base["messages"] = []any{map[string]any{"id": uuid(), "author": map[string]any{"role": "user"}, "create_time": float64(time.Now().UnixMilli()) / 1000, "content": map[string]any{"content_type": "text", "parts": []string{prompt}}, "metadata": map[string]any{"system_hints": []string{"search"}, "developer_mode_connector_ids": []string{}, "selected_github_repos": []string{}, "selected_all_github_repos": false, "serialization_metadata": map[string]any{"custom_symbol_offsets": []any{}}}}}
	base["system_hints"] = []string{}
	base["enable_message_followups"] = true
	base["force_use_search"] = true
	base["client_reported_search_source"] = "conversation_composer_web_icon"
	base["paragen_cot_summary_display_override"] = "allow"
	base["force_parallel_switch"] = "auto"
	data, _ := json.Marshal(base)
	path = "/backend-api/f/conversation"
	h := headers(path)
	h.Set("Accept", "text/event-stream")
	h.Set("X-Conduit-Token", prepared.Token)
	generationDone := callrecord.Span(ctx, "generation_start", "generation_start_ms")
	response, err := s.Do(ctx, http.MethodPost, path, bytes.NewReader(data), h)
	if err != nil {
		generationDone(err)
		return result, err
	}
	if err = checkStatus(response.StatusCode, "search_submit"); err != nil {
		generationDone(err)
		response.Body.Close()
		return result, err
	}
	media, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if e != nil || media != "text/event-stream" {
		generationDone(ErrProtocol)
		response.Body.Close()
		return result, ErrProtocol
	}
	generationDone(nil)
	id, err := readSearchConversationID(ctx, response.Body)
	response.Body.Close()
	if err != nil {
		return result, err
	}
	for {
		path = "/backend-api/conversation/" + id
		h = headers(path)
		h.Set("Referer", s.origin+"/c/"+id)
		h.Set("X-OpenAI-Target-Route", "/backend-api/conversation/{conversation_id}")
		response, e = s.Do(ctx, http.MethodGet, path, nil, h)
		if e != nil {
			return result, e
		}
		status := response.StatusCode
		if status == 404 || status == 409 || status == 423 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
			response.Body.Close()
		} else {
			data, e := readResponse(response, "search_poll", s.policy.MaxResponseBytes)
			if e != nil {
				return result, e
			}
			current, done, e := extractSearchResult(id, data)
			if e != nil {
				return result, e
			}
			if done {
				return current, nil
			}
			result = current
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}
func searchID(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
func readSearchConversationID(ctx context.Context, reader io.Reader) (result string, err error) {
	diagnostics := beginSSEDiagnostics(ctx, "search")
	defer func() { diagnostics.finish(err) }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	id := ""
	var frame strings.Builder
	eventType := ""
	dispatch := func() (bool, error) {
		defer func() { frame.Reset(); eventType = "" }()
		text := strings.TrimSpace(frame.String())
		if text != "" {
			diagnostics.event()
		}
		if eventType == "error" {
			return false, ErrProtocol
		}
		if text == "[DONE]" {
			if id == "" {
				return false, ErrProtocol
			}
			return true, nil
		}
		if text == "" {
			return false, nil
		}
		var value any
		if json.Unmarshal([]byte(text), &value) != nil {
			return false, ErrProtocol
		}
		err := walkSearch(value, 0, func(value any) error {
			item, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			if item["type"] == "error" || item["error"] != nil && item["error"] != false {
				return ErrProtocol
			}
			if raw, ok := item["conversation_id"].(string); ok {
				if !searchID(raw) || id != "" && raw != id {
					return ErrProtocol
				}
				id = raw
			}
			return nil
		})
		return false, err
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if done, err := dispatch(); err != nil {
				return "", err
			} else if done {
				return id, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			if frame.Len()+len(value)+1 > 1<<20 {
				return "", ErrResponseTooLarge
			}
			frame.WriteString(value)
			frame.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		diagnostics.transportError(err)
		return "", safeError(ctx, err)
	}
	if _, err := dispatch(); err != nil {
		return "", err
	}
	if id == "" {
		return "", ErrProtocol
	}
	return id, nil
}
func walkSearch(value any, depth int, visit func(any) error) error {
	if depth > 24 {
		return ErrProtocol
	}
	if err := visit(value); err != nil {
		return err
	}
	switch v := value.(type) {
	case map[string]any:
		for _, child := range v {
			if err := walkSearch(child, depth+1, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := walkSearch(child, depth+1, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
func searchString(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func extractSearchResult(id string, data []byte) (SearchResult, bool, error) {
	result := SearchResult{ConversationID: id, Sources: []SearchSource{}, ImageGroups: []SearchImageGroup{}}
	var document struct {
		Mapping map[string]struct {
			Message map[string]any `json:"message"`
		} `json:"mapping"`
	}
	if json.Unmarshal(data, &document) != nil || document.Mapping == nil || len(document.Mapping) > 10000 {
		return result, false, ErrProtocol
	}
	type candidate struct {
		message  map[string]any
		text     string
		terminal bool
		created  float64
	}
	candidates := []candidate{}
	keys := make([]string, 0, len(document.Mapping))
	for k := range document.Mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		m := document.Mapping[key].Message
		author, _ := m["author"].(map[string]any)
		channel := searchString(m, "channel")
		recipient := searchString(m, "recipient")
		if searchString(author, "role") != "assistant" || channel != "" && channel != "final" || recipient != "" && recipient != "all" {
			continue
		}
		content, _ := m["content"].(map[string]any)
		if searchString(content, "content_type") != "text" {
			continue
		}
		var text strings.Builder
		parts, _ := content["parts"].([]any)
		for _, part := range parts {
			if s, ok := part.(string); ok {
				text.WriteString(s)
			}
		}
		if len(parts) == 0 {
			text.WriteString(searchString(content, "text"))
		}
		if text.Len() == 0 {
			continue
		}
		status := searchString(m, "status")
		if status == "error" || status == "failed" {
			return result, false, ErrProtocol
		}
		metadata, _ := m["metadata"].(map[string]any)
		finish, _ := metadata["finish_details"].(map[string]any)
		done := m["end_turn"] == true || metadata["is_complete"] == true || status == "finished_successfully" || searchString(finish, "type") == "stop"
		created, _ := m["create_time"].(float64)
		candidates = append(candidates, candidate{m, text.String(), done, created})
	}
	if len(candidates) == 0 {
		return result, false, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].terminal != candidates[j].terminal {
			return !candidates[i].terminal
		}
		return candidates[i].created < candidates[j].created
	})
	selected := candidates[len(candidates)-1]
	result.Answer = cleanSearchAnswer(selected.text)
	result.AssistantMessageID = searchString(selected.message, "id")
	result.CreateTime = selected.created
	result.Status = "in_progress"
	if selected.terminal {
		result.Status = "finished_successfully"
	}
	seen := map[string]bool{}
	seenGroups := map[string]bool{}
	addGroup := func(value any) {
		if len(result.ImageGroups) >= 32 {
			return
		}
		group, ok := normalizeSearchImageGroup(value)
		if !ok {
			return
		}
		encoded, _ := json.Marshal(group)
		key := string(encoded)
		if !seenGroups[key] {
			seenGroups[key] = true
			result.ImageGroups = append(result.ImageGroups, group)
		}
	}
	err := walkSearch(selected.message, 0, func(value any) error {
		if text, ok := value.(string); ok {
			// 图片组也可能嵌在正文标记中；提取后再由答案清理器去除内部标记。
			const marker = "\ue200image_group\ue202"
			for len(result.ImageGroups) < 32 {
				start := strings.Index(text, marker)
				if start < 0 {
					break
				}
				text = text[start+len(marker):]
				end := strings.IndexRune(text, '\ue201')
				if end < 0 {
					break
				}
				addGroup(text[:end])
				text = text[end+len("\ue201"):]
			}
			return nil
		}
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		raw := searchString(m, "url")
		if raw == "" {
			raw = searchString(m, "link")
		}
		if raw == "" {
			raw = searchString(m, "source_url")
		}
		u, e := url.Parse(raw)
		if e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && len(raw) <= 8192 && !seen[raw] && len(result.Sources) < 256 {
			seen[raw] = true
			result.Sources = append(result.Sources, SearchSource{Title: searchString(m, "title"), URL: raw, Snippet: searchString(m, "snippet"), SourceType: searchString(m, "type")})
		}
		if group, exists := m["image_group"]; exists {
			addGroup(group)
		}
		kind := searchString(m, "type")
		if kind == "" {
			kind = searchString(m, "name")
		}
		if kind == "" {
			kind = searchString(m, "block_type")
		}
		_, query := m["query"]
		_, ratio := m["aspect_ratio"]
		_, count := m["num_per_query"]
		if strings.EqualFold(strings.TrimSpace(kind), "image_group") || query && (ratio || count) {
			addGroup(m)
		}
		return nil
	})
	return result, selected.terminal && result.Answer != "", err
}

func normalizeSearchImageGroup(value any) (SearchImageGroup, bool) {
	var group SearchImageGroup
	if raw, ok := value.(string); ok {
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &value) != nil {
			return group, false
		}
	}
	m, ok := value.(map[string]any)
	if !ok {
		return group, false
	}
	queries := m["query"]
	if queries == nil || queries == "" {
		queries = m["queries"]
	} else if list, ok := queries.([]any); ok && len(list) == 0 {
		queries = m["queries"]
	}
	if text, ok := queries.(string); ok {
		queries = []any{text}
	}
	list, ok := queries.([]any)
	if !ok {
		return group, false
	}
	for _, item := range list {
		text, ok := item.(string)
		text = strings.TrimSpace(text)
		if ok && text != "" && len(text) <= 2048 {
			group.Queries = append(group.Queries, text)
			if len(group.Queries) == 16 {
				break
			}
		}
	}
	group.AspectRatio = strings.TrimSpace(searchString(m, "aspect_ratio"))
	if n, ok := m["num_per_query"].(float64); ok && n >= 1 && n <= 16 {
		group.NumPerQuery = int(n)
	}
	return group, len(group.Queries) > 0
}

func cleanSearchAnswer(s string) string {
	var out strings.Builder
	for {
		i := strings.IndexRune(s, '\ue200')
		if i < 0 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:i])
		rest := s[i+len("\ue200"):]
		j := strings.IndexRune(rest, '\ue201')
		if j < 0 {
			break
		}
		block := strings.Split(rest[:j], "\ue202")
		if len(block) > 2 && block[0] == "url" {
			out.WriteString(block[1] + " (" + block[2] + ")")
		}
		s = rest[j+len("\ue201"):]
	}
	return strings.TrimSpace(out.String())
}
