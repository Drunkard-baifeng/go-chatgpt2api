// Package prompts owns the verified Prompt Library, source settings and snapshots.
package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Item struct {
	ID                 string   `json:"id"`
	SourceID           string   `json:"source_id"`
	SourceName         string   `json:"source_name"`
	Title              string   `json:"title"`
	Prompt             string   `json:"prompt"`
	Description        string   `json:"description"`
	Preview            string   `json:"preview"`
	Link               string   `json:"link"`
	Author             string   `json:"author"`
	Category           string   `json:"category"`
	SubCategory        string   `json:"sub_category"`
	Tags               []string `json:"tags"`
	ReferenceImageURLs []string `json:"reference_image_urls"`
	ImageMode          string   `json:"image_mode"`
	ImageModel         string   `json:"image_model"`
	ImageSize          string   `json:"image_size"`
	ImageCount         *int     `json:"image_count"`
	CreatedAt          string   `json:"created_at"`
}

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Homepage    string `json:"homepage"`
	Enabled     bool   `json:"enabled"`
	BuiltIn     bool   `json:"built_in"`
	SortOrder   int    `json:"sort_order"`
	PromptCount int    `json:"prompt_count"`
	Cached      bool   `json:"cached"`
	SyncState   string `json:"sync_state"`
	SyncLabel   string `json:"sync_label"`
	SyncMessage string `json:"sync_message"`
	SyncTone    string `json:"sync_tone"`
	LastSyncAt  string `json:"last_sync_at"`
	LastError   string `json:"last_error"`
	LastFetchMS *int   `json:"last_fetch_ms"`
}

type SourceError struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type SyncSummary struct {
	Status    string `json:"status"`
	Tone      string `json:"tone"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Message   string `json:"message"`
}
type View struct {
	SchemaVersion       int           `json:"schema_version"`
	GeneratedAt         string        `json:"generated_at"`
	Revision            string        `json:"revision"`
	RegistryRevision    string        `json:"registry_revision"`
	RegistryGeneratedAt string        `json:"registry_generated_at"`
	Synced              bool          `json:"synced"`
	PromptCount         int           `json:"prompt_count"`
	SourceCount         int           `json:"source_count"`
	EnabledSourceCount  int           `json:"enabled_source_count"`
	CachedSourceCount   int           `json:"cached_source_count"`
	SourceErrorCount    int           `json:"source_error_count"`
	SyncSummary         SyncSummary   `json:"sync_summary"`
	SourceErrors        []SourceError `json:"source_errors"`
	Items               []Item        `json:"items"`
	Sources             []Source      `json:"sources"`
}

type registrySource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Homepage    string `json:"homepage"`
	UpstreamURL string `json:"upstream_url"`
	Path        string `json:"path"`
	Count       int    `json:"count"`
	SHA256      string `json:"sha256"`
}
type setting struct {
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
	UpdatedAt string `json:"updated_at"`
}
type sourceStatus struct {
	ContentRevision string `json:"content_revision"`
	LastSyncAt      string `json:"last_sync_at"`
	LastError       string `json:"last_error"`
	LastFetchMS     *int   `json:"last_fetch_ms"`
}
type registryState struct {
	Revision    string           `json:"revision"`
	GeneratedAt string           `json:"generated_at"`
	Sources     []registrySource `json:"sources"`
}
type snapshot struct {
	SchemaVersion int                     `json:"schema_version"`
	UpdatedAt     string                  `json:"updated_at"`
	Verified      bool                    `json:"snapshot_verified"`
	Digest        string                  `json:"snapshot_digest"`
	Registry      registryState           `json:"registry"`
	Items         map[string][]Item       `json:"items_by_source"`
	Status        map[string]sourceStatus `json:"source_status"`
}
type settingsState struct {
	SchemaVersion int       `json:"schema_version"`
	Sources       []setting `json:"sources"`
}

func defaultSources() []registrySource {
	return []registrySource{
		{ID: "banana-prompt-quicker", Name: "Banana Prompt Quicker", Homepage: "https://glidea.github.io/banana-prompt-quicker/", UpstreamURL: "https://glidea.github.io/banana-prompt-quicker/prompts.json", Path: "sources/banana-prompt-quicker.json"},
		{ID: "davidwu-gpt-image2-prompts", Name: "DavidWu GPT Image 2 Prompts", Homepage: "https://github.com/davidwuw0811-boop/awesome-gpt-image2-prompts", UpstreamURL: "https://raw.githubusercontent.com/davidwuw0811-boop/awesome-gpt-image2-prompts/main/prompts.json", Path: "sources/davidwu-gpt-image2-prompts.json"},
		{ID: "awesome-gpt-image", Name: "Awesome GPT Image", Homepage: "https://github.com/ZeroLu/awesome-gpt-image", UpstreamURL: "https://raw.githubusercontent.com/ZeroLu/awesome-gpt-image/main/README.zh-CN.md", Path: "sources/awesome-gpt-image.json"},
		{ID: "awesome-gpt4o-image-prompts", Name: "Awesome GPT-4o Image Prompts", Homepage: "https://github.com/ImgEdify/Awesome-GPT4o-Image-Prompts", UpstreamURL: "https://raw.githubusercontent.com/ImgEdify/Awesome-GPT4o-Image-Prompts/main/README.zh-CN.md", Path: "sources/awesome-gpt4o-image-prompts.json"},
		{ID: "youmind-gpt-image-2", Name: "YouMind GPT Image 2", Homepage: "https://github.com/YouMind-OpenLab/awesome-gpt-image-2", UpstreamURL: "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-gpt-image-2/main/README_zh.md", Path: "sources/youmind-gpt-image-2.json"},
		{ID: "youmind-nano-banana-pro", Name: "YouMind Nano Banana Pro", Homepage: "https://github.com/YouMind-OpenLab/awesome-nano-banana-pro-prompts", UpstreamURL: "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-nano-banana-pro-prompts/main/README_zh.md", Path: "sources/youmind-nano-banana-pro.json"},
	}
}
func sourceSetting(settings map[string]setting, id string, index int) setting {
	if v, ok := settings[id]; ok {
		return v
	}
	enabled := false
	for _, source := range defaultSources() {
		if source.ID == id {
			enabled = true
			break
		}
	}
	return setting{ID: id, Enabled: enabled, SortOrder: index * 10}
}
func contentRevision(items []Item) string {
	h := sha256.New()
	for _, item := range items {
		h.Write([]byte(item.ID))
		h.Write([]byte{0})
		h.Write([]byte(item.Prompt))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func project(state snapshot, settings map[string]setting) View {
	v := View{SchemaVersion: 1, GeneratedAt: now(), RegistryRevision: state.Registry.Revision, RegistryGeneratedAt: state.Registry.GeneratedAt, Items: []Item{}, Sources: []Source{}, SourceErrors: []SourceError{}}
	type ordered struct {
		source  registrySource
		setting setting
	}
	orderedSources := make([]ordered, 0, len(state.Registry.Sources))
	for index, source := range state.Registry.Sources {
		orderedSources = append(orderedSources, ordered{source, sourceSetting(settings, source.ID, index)})
	}
	sort.SliceStable(orderedSources, func(i, j int) bool {
		a, b := orderedSources[i], orderedSources[j]
		if a.setting.SortOrder != b.setting.SortOrder {
			return a.setting.SortOrder < b.setting.SortOrder
		}
		if strings.ToLower(a.source.Name) != strings.ToLower(b.source.Name) {
			return strings.ToLower(a.source.Name) < strings.ToLower(b.source.Name)
		}
		return a.source.ID < b.source.ID
	})
	revisions := []any{}
	for _, entry := range orderedSources {
		raw, config := entry.source, entry.setting
		items := state.Items[raw.ID]
		status := state.Status[raw.ID]
		source := Source{ID: raw.ID, Name: raw.Name, URL: raw.UpstreamURL, Homepage: raw.Homepage, Enabled: config.Enabled, BuiltIn: true, SortOrder: config.SortOrder, PromptCount: len(items), Cached: len(items) > 0, LastSyncAt: status.LastSyncAt, LastError: status.LastError, LastFetchMS: copyInt(status.LastFetchMS)}
		switch {
		case !source.Enabled:
			source.SyncState = "disabled"
			source.SyncLabel = "已停用"
			source.SyncMessage = "提示词源已停用"
			source.SyncTone = "muted"
		case source.LastError != "" && source.Cached:
			source.SyncState = "cached"
			source.SyncLabel = "缓存可用"
			source.SyncMessage = "同步失败，继续使用本地缓存"
			source.SyncTone = "warning"
		case source.LastError != "":
			source.SyncState = "failed"
			source.SyncLabel = "同步失败"
			source.SyncMessage = "同步失败，当前没有可用缓存"
			source.SyncTone = "danger"
		case source.Cached || source.LastSyncAt != "":
			source.SyncState = "synced"
			source.SyncLabel = "已同步"
			source.SyncMessage = "提示词源已更新到本地"
			source.SyncTone = "success"
		default:
			source.SyncState = "pending"
			source.SyncLabel = "待同步"
			source.SyncMessage = "提示词源等待首次同步"
			source.SyncTone = "muted"
		}
		if source.Enabled {
			v.EnabledSourceCount++
			for _, item := range items {
				v.Items = append(v.Items, cloneItem(item))
			}
			if source.LastError != "" {
				v.SourceErrors = append(v.SourceErrors, SourceError{ID: source.ID, Name: source.Name, Error: source.LastError})
			}
		}
		if source.Cached {
			v.CachedSourceCount++
		}
		v.Sources = append(v.Sources, source)
		revisions = append(revisions, map[string]any{"id": source.ID, "enabled": source.Enabled, "sort_order": source.SortOrder, "content_revision": status.ContentRevision, "last_error": status.LastError})
	}
	raw, _ := canonicalJSON(map[string]any{"registry_revision": v.RegistryRevision, "sources": revisions})
	digest := sha256.Sum256(raw)
	v.Revision = hex.EncodeToString(digest[:])[:24]
	v.PromptCount = len(v.Items)
	v.SourceCount = len(v.Sources)
	v.SourceErrorCount = len(v.SourceErrors)
	v.Synced = v.CachedSourceCount > 0
	v.SyncSummary = SyncSummary{Status: "success", Tone: "success", Total: v.EnabledSourceCount, Succeeded: v.EnabledSourceCount - v.SourceErrorCount, Failed: v.SourceErrorCount}
	switch {
	case v.SourceErrorCount > 0 && v.SyncSummary.Succeeded == 0:
		v.SyncSummary.Status = "failed"
		v.SyncSummary.Tone = "danger"
		v.SyncSummary.Message = fmt.Sprintf("词源同步失败：%d 个词源失败，已保留可用的本地缓存", v.SourceErrorCount)
	case v.SourceErrorCount > 0:
		v.SyncSummary.Status = "partial"
		v.SyncSummary.Tone = "warning"
		v.SyncSummary.Message = fmt.Sprintf("词源同步完成：成功 %d，失败 %d，共 %d 条提示词", v.SyncSummary.Succeeded, v.SourceErrorCount, v.PromptCount)
	case v.EnabledSourceCount == 0:
		v.SyncSummary.Message = "没有启用的提示词源"
	default:
		v.SyncSummary.Message = fmt.Sprintf("词源同步完成：成功 %d，共 %d 条提示词", v.SyncSummary.Succeeded, v.PromptCount)
	}
	return v
}
func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}
func cloneItem(item Item) Item {
	item.Tags = append([]string{}, item.Tags...)
	item.ReferenceImageURLs = append([]string{}, item.ReferenceImageURLs...)
	item.ImageCount = copyInt(item.ImageCount)
	return item
}
