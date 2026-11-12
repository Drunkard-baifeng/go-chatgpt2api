package images

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"time"

	"chatgpt2api/go_backend/internal/managedhttp"
)

type GenBoxError struct {
	Status        int
	Code, Message string
}

func (e GenBoxError) Error() string { return e.Message }
func genBoxError(code string) error {
	status := 502
	message := "GenBox 返回的数据无效"
	switch code {
	case "genbox_not_configured":
		status = 409
		message = "GenBox 尚未配置，请先填写并启用"
	case "genbox_probe_rejected":
		message = "GenBox 连接验证未通过"
	case "genbox_unavailable":
		message = "GenBox 服务当前不可用"
	case "genbox_rejected":
		message = "GenBox 未接受该图片"
	case "genbox_request_failed":
		status = 504
		message = "GenBox 请求失败或超时"
	}
	return GenBoxError{Status: status, Code: code, Message: message}
}

type genBoxConfiguration struct {
	URL, Source, Key string
	Timeout          time.Duration
	Auto             bool
}

func genBoxSettings(raw map[string]any) (genBoxConfiguration, error) {
	var cfg genBoxConfiguration
	m, _ := raw["genbox_push"].(map[string]any)
	enabled, _ := m["enabled"].(bool)
	if !enabled {
		return cfg, genBoxError("genbox_not_configured")
	}
	var e error
	if cfg.URL, e = cleanIntegrationString(m["base_url"], 2048); e != nil {
		return cfg, genBoxError("genbox_not_configured")
	}
	u, e := managedhttp.ParseURL(cfg.URL)
	if e != nil {
		return cfg, genBoxError("genbox_not_configured")
	}
	cfg.URL = u.String()
	if cfg.Source, e = cleanIntegrationString(m["source_id"], 256); e != nil || cfg.Source == "" {
		return cfg, genBoxError("genbox_not_configured")
	}
	if cfg.Key, e = cleanIntegrationString(m["push_key"], 4096); e != nil || cfg.Key == "" {
		return cfg, genBoxError("genbox_not_configured")
	}
	secs := int64(20)
	switch v := m["timeout_secs"].(type) {
	case int:
		secs = int64(v)
	case int64:
		secs = v
	case float64:
		if v != float64(int64(v)) {
			return cfg, genBoxError("genbox_not_configured")
		}
		secs = int64(v)
	case json.Number:
		secs, e = v.Int64()
		if e != nil {
			return cfg, genBoxError("genbox_not_configured")
		}
	case nil:
	default:
		return cfg, genBoxError("genbox_not_configured")
	}
	if secs < 5 || secs > 120 {
		return cfg, genBoxError("genbox_not_configured")
	}
	cfg.Timeout = time.Duration(secs) * time.Second
	cfg.Auto, _ = m["auto_push_after_studio"].(bool)
	return cfg, nil
}
func genBoxObject(data []byte) (map[string]any, error) {
	if len(data) > 1<<20 || rejectDuplicateCatalogKeys(data) != nil {
		return nil, genBoxError("genbox_invalid_receipt")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var out map[string]any
	if d.Decode(&out) != nil || out == nil {
		return nil, genBoxError("genbox_invalid_receipt")
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return nil, genBoxError("genbox_invalid_receipt")
	}
	return out, nil
}

type GenBoxPushResult struct {
	Path string `json:"path"`
	GenBoxPushState
	SourceRetained bool `json:"source_retained"`
}

func (s *ImageStorageService) PushGenBox(parent context.Context, p string, metadata map[string]string) (GenBoxPushResult, error) {
	return s.pushGenBox(parent, p, metadata, "")
}
func (s *ImageStorageService) pushGenBox(parent context.Context, p string, metadata map[string]string, expectedGeneration string) (GenBoxPushResult, error) {
	if !assetPath.MatchString(p) {
		return GenBoxPushResult{}, ErrGalleryInput
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	defer done()
	raw, e := s.runtimeSettings(ctx)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	cfg, e := genBoxSettings(raw)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	lock := s.assetMutex(p)
	lock.Lock()
	defer lock.Unlock()
	entry, e := s.snapshotEntry(p)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	if expectedGeneration != "" && entry.Generation != expectedGeneration {
		return GenBoxPushResult{}, ErrAssets
	}
	data, e := s.bytesForEntry(ctx, p, entry)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	if _, e = Inspect(data); e != nil {
		return GenBoxPushResult{}, ErrAssets
	}
	hash := assetHash(data)
	client, e := managedhttp.New(cfg.URL)
	if e != nil {
		return GenBoxPushResult{}, genBoxError("genbox_not_configured")
	}
	defer client.Close()
	headers := http.Header{"X-Genbox-Source": []string{cfg.Source}, "X-Genbox-Key": []string{cfg.Key}}
	request := func(method, path string, body io.Reader, headers http.Header) ([]byte, int, error) {
		data, status, e := client.Request(ctx, method, path, body, headers, 1<<20)
		if e != nil {
			if parent.Err() != nil {
				return nil, status, parent.Err()
			}
			return nil, status, genBoxError("genbox_request_failed")
		}
		return data, status, nil
	}
	probe, status, e := request("GET", "/api/sync/push/status", nil, headers)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	if status < 200 || status >= 300 {
		return GenBoxPushResult{}, genBoxError("genbox_unavailable")
	}
	value, e := genBoxObject(probe)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	maximum, ok := value["max_image_bytes"].(json.Number)
	maxBytes, err := maximum.Int64()
	if value["ok"] != true || value["contract_version"] != "v1" || value["source_id"] != cfg.Source || !ok || err != nil || maxBytes < int64(len(data)) {
		return GenBoxPushResult{}, genBoxError("genbox_probe_rejected")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if writer.WriteField("remote_path", p) != nil || writer.WriteField("source_sha256", hash) != nil {
		return GenBoxPushResult{}, ErrAssets
	}
	for _, key := range []string{"prompt", "created_at", "date", "model"} {
		if value := metadata[key]; value != "" {
			if len(value) > 64<<10 {
				return GenBoxPushResult{}, ErrGalleryInput
			}
			if writer.WriteField(key, value) != nil {
				return GenBoxPushResult{}, ErrAssets
			}
		}
	}
	part, e := writer.CreateFormFile("image", path.Base(p))
	if e != nil {
		return GenBoxPushResult{}, ErrAssets
	}
	if _, e = part.Write(data); e != nil {
		return GenBoxPushResult{}, ErrAssets
	}
	if e = writer.Close(); e != nil {
		return GenBoxPushResult{}, ErrAssets
	}
	headers.Set("Content-Type", writer.FormDataContentType())
	receipt, status, e := request("POST", "/api/sync/push", &body, headers)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	if status < 200 || status >= 300 {
		return GenBoxPushResult{}, genBoxError("genbox_rejected")
	}
	value, e = genBoxObject(receipt)
	if e != nil {
		return GenBoxPushResult{}, e
	}
	stateStatus, _ := value["status"].(string)
	label := genBoxLabel(stateStatus)
	if value["ok"] != true || value["contract_version"] != "v1" || value["source_id"] != cfg.Source || value["sha256"] != hash || label == "" {
		return GenBoxPushResult{}, genBoxError("genbox_invalid_receipt")
	}
	state := GenBoxPushState{Status: stateStatus, Label: label, SHA256: hash, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if !ok || pending || current.Generation != entry.Generation {
		return GenBoxPushResult{}, ErrAssets
	}
	current.Genbox = &state
	next := s.catalog.clone()
	next.Entries[p] = current
	if e = s.commit(next); e != nil {
		return GenBoxPushResult{}, e
	}
	return GenBoxPushResult{Path: p, GenBoxPushState: state, SourceRetained: true}, nil
}

type GenBoxPushState struct {
	Status    string `json:"status"`
	Label     string `json:"label"`
	SHA256    string `json:"sha256"`
	UpdatedAt string `json:"updated_at"`
}

func genBoxLabel(status string) string {
	switch status {
	case "imported":
		return "已推送到 GenBox"
	case "already-imported":
		return "GenBox 已存在"
	case "duplicate-local":
		return "GenBox 本地重复"
	}
	return ""
}

func validGenBoxState(state *GenBoxPushState) bool {
	if state == nil {
		return false
	}
	_, e := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	return e == nil && sha256Pattern.MatchString(state.SHA256) && genBoxLabel(state.Status) != "" && state.Label == genBoxLabel(state.Status)
}
