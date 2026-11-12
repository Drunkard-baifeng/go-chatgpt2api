package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"chatgpt2api/go_backend/internal/managedhttp"
)

var ErrStorageConfig = errors.New("图片存储未启用或配置无效")
var ErrStorageRemote = errors.New("图片远端存储请求失败")
var ErrStorageBusy = errors.New("图片存储操作繁忙，请稍后重试")
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type remoteLocation struct {
	URL     string `json:"url"`
	Root    string `json:"root"`
	SHA256  string `json:"sha256,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}
type storageConfiguration struct{ Mode, URL, Root, Username, Password, PublicURL string }
type webdavClient struct {
	client   *managedhttp.Client
	settings storageConfiguration
}

func cleanIntegrationString(v any, maximum int) (string, error) {
	s, ok := v.(string)
	if v == nil {
		return "", nil
	}
	if !ok || len(s) > maximum || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", ErrStorageConfig
	}
	return s, nil
}
func cleanRoot(raw string) (string, error) {
	raw = strings.Trim(raw, "/")
	if len(raw) > 1024 || strings.ContainsAny(raw, "\\\x00\r\n?#") {
		return "", ErrStorageConfig
	}
	if raw == "" {
		return "", nil
	}
	for _, part := range strings.Split(raw, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrStorageConfig
		}
	}
	return raw, nil
}
func (s *ImageStorageService) runtimeSettings(ctx context.Context) (map[string]any, error) {
	s.mu.Lock()
	read := s.runtime
	s.mu.Unlock()
	if read == nil {
		return map[string]any{}, nil
	}
	raw, e := read(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrAssets
	}
	return raw, nil
}
func storageSettings(raw map[string]any) (storageConfiguration, error) {
	out := storageConfiguration{Mode: "local"}
	m, _ := raw["image_storage"].(map[string]any)
	if m == nil {
		return out, nil
	}
	enabled, _ := m["enabled"].(bool)
	var e error
	fields := []struct {
		name   string
		target *string
		limit  int
	}{{"mode", &out.Mode, 16}, {"webdav_url", &out.URL, 2048}, {"webdav_root_path", &out.Root, 1024}, {"webdav_username", &out.Username, 1024}, {"webdav_password", &out.Password, 4096}, {"public_base_url", &out.PublicURL, 2048}}
	for _, field := range fields {
		if *field.target, e = cleanIntegrationString(m[field.name], field.limit); e != nil {
			return out, e
		}
	}
	if out.Mode == "" {
		out.Mode = "local"
	}
	if out.Mode != "local" && out.Mode != "webdav" && out.Mode != "both" {
		return out, ErrStorageConfig
	}
	if out.Root, e = cleanRoot(out.Root); e != nil {
		return out, e
	}
	if out.URL != "" {
		u, e := managedhttp.ParseURL(out.URL)
		if e != nil {
			return out, ErrStorageConfig
		}
		out.URL = u.String()
	}
	if out.PublicURL != "" {
		u, e := managedhttp.ParseURL(out.PublicURL)
		if e != nil {
			return out, ErrStorageConfig
		}
		out.PublicURL = u.String()
	}
	if !enabled {
		out.Mode = "local"
	}
	if out.Mode != "local" && out.URL == "" {
		return out, ErrStorageConfig
	}
	return out, nil
}
func (s *ImageStorageService) storageConfig(ctx context.Context) (storageConfiguration, error) {
	raw, e := s.runtimeSettings(ctx)
	if e != nil {
		return storageConfiguration{}, e
	}
	return storageSettings(raw)
}
func newWebDAV(settings storageConfiguration, location *remoteLocation) (*webdavClient, error) {
	if settings.URL == "" {
		return nil, ErrStorageConfig
	}
	if location != nil && (settings.URL != location.URL || settings.Root != location.Root) {
		return nil, ErrStorageConfig
	}
	client, e := managedhttp.New(settings.URL)
	if e != nil {
		return nil, ErrStorageConfig
	}
	return &webdavClient{client: client, settings: settings}, nil
}
func (c *webdavClient) close() { c.client.Close() }
func (c *webdavClient) headers() http.Header {
	h := http.Header{}
	if c.settings.Username != "" || c.settings.Password != "" {
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.settings.Username+":"+c.settings.Password)))
	}
	return h
}
func (c *webdavClient) remotePath(rel string) string {
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(c.settings.Root+"/"+rel, "/"), "/") {
		parts = append(parts, url.PathEscape(part))
	}
	return "/" + strings.Join(parts, "/")
}
func (c *webdavClient) request(ctx context.Context, method, rel string, data []byte, limit int64) ([]byte, int, error) {
	h := c.headers()
	if method == "PUT" {
		h.Set("Content-Type", http.DetectContentType(data))
	}
	b, status, e := c.client.Request(ctx, method, c.remotePath(rel), bytes.NewReader(data), h, limit)
	if e != nil {
		if ctx.Err() != nil {
			return nil, status, ctx.Err()
		}
		return nil, status, ErrStorageRemote
	}
	return b, status, nil
}
func (c *webdavClient) put(ctx context.Context, rel string, data []byte) error {
	directory := path.Dir(strings.Trim(c.settings.Root+"/"+rel, "/"))
	current := ""
	for _, part := range strings.Split(directory, "/") {
		if part == "" || part == "." {
			continue
		}
		current += "/" + url.PathEscape(part)
		_, status, e := c.client.Request(ctx, "MKCOL", current, nil, c.headers(), 64<<10)
		if e != nil || status != 201 && status != 405 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrStorageRemote
		}
	}
	_, status, e := c.request(ctx, "PUT", rel, data, 64<<10)
	if e != nil {
		return e
	}
	if status < 200 || status >= 300 {
		return ErrStorageRemote
	}
	return nil
}
func (c *webdavClient) get(ctx context.Context, rel string) ([]byte, error) {
	b, status, e := c.request(ctx, "GET", rel, nil, maxAssetBytes)
	if e != nil {
		return nil, e
	}
	if status != 200 {
		return nil, ErrStorageRemote
	}
	return b, nil
}
func (c *webdavClient) delete(ctx context.Context, rel string) error {
	_, status, e := c.request(ctx, "DELETE", rel, nil, 64<<10)
	if e != nil {
		return e
	}
	if status != 200 && status != 202 && status != 204 && status != 404 {
		return ErrStorageRemote
	}
	return nil
}

type ImageStorageTestResult struct {
	OK     bool    `json:"ok"`
	Status int     `json:"status"`
	Error  *string `json:"error"`
}
type ImageStorageSyncResult struct {
	Uploaded int `json:"uploaded"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
}

func (s *ImageStorageService) TestWebDAV(parent context.Context) (ImageStorageTestResult, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return ImageStorageTestResult{}, e
	}
	defer done()
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return ImageStorageTestResult{}, e
	}
	client, e := newWebDAV(cfg, nil)
	if e != nil {
		return ImageStorageTestResult{}, e
	}
	defer client.close()
	id, e := randomID()
	if e != nil {
		return ImageStorageTestResult{}, e
	}
	rel := ".chatgpt2api-probe-" + id + ".txt"
	payload := []byte("chatgpt2api WebDAV probe\n")
	cleaned := false
	// 取消后的清理使用独立短期限，避免把探测对象留在已配置的存储中。
	defer func() {
		if !cleaned {
			cleanup, cancel := context.WithTimeout(s.storageCtx, 5*time.Second)
			defer cancel()
			_ = client.delete(cleanup, rel)
		}
	}()
	if e = client.put(ctx, rel, payload); e != nil {
		return ImageStorageTestResult{}, e
	}
	data, e := client.get(ctx, rel)
	if e != nil {
		return ImageStorageTestResult{}, e
	}
	if !bytes.Equal(data, payload) {
		return ImageStorageTestResult{}, ErrStorageRemote
	}
	if e = client.delete(ctx, rel); e != nil {
		return ImageStorageTestResult{}, e
	}
	cleaned = true
	return ImageStorageTestResult{OK: true, Status: 200}, nil
}
