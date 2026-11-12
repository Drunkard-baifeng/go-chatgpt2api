package prompts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	manifestLimit      int64 = 128 << 10
	payloadLimit       int64 = 8 << 20
	maxItems                 = 20000
	maxSources               = 64
	defaultRegistryURL       = "https://raw.githubusercontent.com/yukkcat/image-prompts/main/dist"
)

var sourceIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var pathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)
var markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
var markdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)

func decodeJSON(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return ErrRegistry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(destination) != nil {
		return ErrRegistry
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return ErrRegistry
	}
	return nil
}

// canonicalJSON matches the registry's Python JSON encoding: sorted keys,
// compact separators, UTF-8, and no HTML or U+2028/U+2029 escaping.
func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	// Replace only JSON string escapes, never a literal backslash in prompt text.
	var generic any
	if err := decodeJSON(encoded, &generic); err != nil {
		return nil, err
	}
	return canonicalValue(generic)
}
func canonicalValue(value any) ([]byte, error) {
	switch v := value.(type) {
	case string:
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(v); err != nil {
			return nil, err
		}
		data := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
		result := make([]byte, 0, len(data))
		for i := 0; i < len(data); i++ {
			if data[i] == '\\' && i+1 < len(data) {
				if data[i+1] == '\\' {
					result = append(result, data[i], data[i+1])
					i++
					continue
				}
				if i+5 < len(data) && string(data[i:i+6]) == `\u2028` {
					result = append(result, []byte("\u2028")...)
					i += 5
					continue
				}
				if i+5 < len(data) && string(data[i:i+6]) == `\u2029` {
					result = append(result, []byte("\u2029")...)
					i += 5
					continue
				}
			}
			result = append(result, data[i])
		}
		return result, nil
	case []any:
		var buffer bytes.Buffer
		buffer.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buffer.WriteByte(',')
			}
			data, err := canonicalValue(item)
			if err != nil {
				return nil, err
			}
			buffer.Write(data)
		}
		buffer.WriteByte(']')
		return buffer.Bytes(), nil
	case map[string]any:
		// encoding/json sorts map keys; decoding each fragment above only affects strings.
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var buffer bytes.Buffer
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			name, _ := canonicalValue(key)
			buffer.Write(name)
			buffer.WriteByte(':')
			data, err := canonicalValue(v[key])
			if err != nil {
				return nil, err
			}
			buffer.Write(data)
		}
		buffer.WriteByte('}')
		return buffer.Bytes(), nil
	default:
		return json.Marshal(value)
	}
}
func validURL(raw string, empty bool) (*url.URL, error) {
	if raw == "" && empty {
		return &url.URL{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 16384 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(u.Hostname(), "%\\") {
		return nil, ErrRegistry
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, ErrRegistry
		}
	}
	return u, nil
}
func safePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || !pathPattern.MatchString(path) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}
func cleanInline(value string) string { return strings.Join(strings.Fields(value), " ") }
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
func cleanDisplay(value string) string {
	value = html.UnescapeString(value)
	value = markdownImagePattern.ReplaceAllString(value, " ")
	value = markdownLinkPattern.ReplaceAllString(value, "$1")
	return cleanInline(htmlTagPattern.ReplaceAllString(value, " "))
}
func imageMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "generate", "image", "text-to-image", "t2i":
		return "generate"
	case "edit", "image-to-image", "i2i":
		return "edit"
	}
	return ""
}
func stringValue(raw map[string]any, key string) (string, bool) {
	value, ok := raw[key].(string)
	return strings.TrimSpace(value), ok
}
func integer(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(string(number), 10, 32)
	return int(n), err == nil
}
func stringList(value any, max int) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, ErrRegistry
	}
	if len(values) > max {
		return nil, ErrRegistry
	}
	result := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		text, ok := raw.(string)
		text = strings.TrimSpace(text)
		if !ok || len(text) > 16384 {
			return nil, ErrRegistry
		}
		if text != "" && !seen[text] {
			seen[text] = true
			result = append(result, text)
		}
	}
	return result, nil
}

type manifest struct {
	Revision    string
	GeneratedAt string
	PromptsPath string
	Total       int
	Sources     []registrySource
	RawSources  []any
}

func parseManifest(data []byte) (manifest, error) {
	var m manifest
	var raw map[string]any
	if int64(len(data)) > manifestLimit || decodeJSON(data, &raw) != nil || raw == nil {
		return m, ErrRegistry
	}
	version, ok := integer(raw["schemaVersion"])
	if !ok || version != 1 {
		return m, ErrRegistry
	}
	m.Revision, _ = stringValue(raw, "registryHash")
	if !hashPattern.MatchString(m.Revision) {
		return m, ErrRegistry
	}
	m.GeneratedAt, _ = stringValue(raw, "generatedAt")
	m.PromptsPath, _ = stringValue(raw, "promptsPath")
	m.Total, ok = integer(raw["total"])
	if !ok || m.Total < 0 || m.Total > maxItems || m.GeneratedAt == "" || len(m.GeneratedAt) > 100 || !safePath(m.PromptsPath) {
		return m, ErrRegistry
	}
	m.RawSources, ok = raw["sources"].([]any)
	if !ok || len(m.RawSources) == 0 || len(m.RawSources) > maxSources {
		return m, ErrRegistry
	}
	seen := map[string]bool{}
	total := 0
	for _, value := range m.RawSources {
		rawSource, ok := value.(map[string]any)
		if !ok {
			return m, ErrRegistry
		}
		source := registrySource{}
		source.ID, _ = stringValue(rawSource, "id")
		source.Name, _ = stringValue(rawSource, "name")
		source.Name = truncate(cleanInline(source.Name), 120)
		source.Homepage, _ = stringValue(rawSource, "homepage")
		source.UpstreamURL, _ = stringValue(rawSource, "upstreamUrl")
		source.Path, _ = stringValue(rawSource, "path")
		source.SHA256, _ = stringValue(rawSource, "sha256")
		source.Count, ok = integer(rawSource["count"])
		if !ok || source.Count < 0 || source.Count > maxItems || !sourceIDPattern.MatchString(source.ID) || seen[source.ID] || source.Name == "" || !safePath(source.Path) || !hashPattern.MatchString(source.SHA256) {
			return m, ErrRegistry
		}
		if _, err := validURL(source.Homepage, false); err != nil {
			return m, err
		}
		if _, err := validURL(source.UpstreamURL, false); err != nil {
			return m, err
		}
		seen[source.ID] = true
		total += source.Count
		m.Sources = append(m.Sources, source)
	}
	if total != m.Total {
		return m, ErrRegistry
	}
	return m, nil
}
func parseRegistry(manifestBytes, payload []byte) (snapshot, error) {
	m, err := parseManifest(manifestBytes)
	if err != nil {
		return snapshot{}, err
	}
	return parsePayload(m, payload)
}
func parsePayload(m manifest, payload []byte) (snapshot, error) {
	state := snapshot{SchemaVersion: 1, UpdatedAt: now(), Verified: true, Registry: registryState{Revision: m.Revision, GeneratedAt: m.GeneratedAt, Sources: m.Sources}, Items: map[string][]Item{}, Status: map[string]sourceStatus{}}
	if int64(len(payload)) > payloadLimit {
		return state, ErrRegistry
	}
	sourcesBytes, err := canonicalJSON(m.RawSources)
	if err != nil {
		return state, ErrRegistry
	}
	h := sha256.New()
	h.Write(sourcesBytes)
	h.Write(payload)
	if hex.EncodeToString(h.Sum(nil)) != m.Revision {
		return state, ErrRegistry
	}
	var items []any
	if decodeJSON(payload, &items) != nil || items == nil || len(items) != m.Total {
		return state, ErrRegistry
	}
	sources := map[string]registrySource{}
	for _, source := range m.Sources {
		sources[source.ID] = source
		state.Items[source.ID] = []Item{}
	}
	seen := map[string]bool{}
	for _, raw := range items {
		value, ok := raw.(map[string]any)
		if !ok {
			return state, ErrRegistry
		}
		sourceID, _ := stringValue(value, "sourceId")
		source, ok := sources[sourceID]
		if !ok {
			return state, ErrRegistry
		}
		item, err := parseItem(value, source)
		if err != nil || seen[item.ID] {
			return state, ErrRegistry
		}
		seen[item.ID] = true
		state.Items[sourceID] = append(state.Items[sourceID], item)
	}
	for _, source := range m.Sources {
		if len(state.Items[source.ID]) != source.Count {
			return state, ErrRegistry
		}
	}
	return state, nil
}
func parseItem(raw map[string]any, source registrySource) (Item, error) {
	item := Item{SourceID: source.ID, SourceName: source.Name}
	required := []string{"id", "sourceId", "title", "prompt", "description", "coverUrl", "referenceImageUrls", "tags", "author", "sourceUrl", "createdAt", "imageMode", "imageModel"}
	allowed := map[string]bool{"imageSize": true, "imageCount": true}
	for _, key := range required {
		allowed[key] = true
		if _, ok := raw[key]; !ok {
			return item, ErrRegistry
		}
	}
	for key := range raw {
		if !allowed[key] {
			return item, ErrRegistry
		}
	}
	for _, key := range []string{"id", "sourceId", "title", "prompt", "description", "coverUrl", "author", "sourceUrl", "createdAt", "imageMode", "imageModel", "imageSize"} {
		if value, ok := raw[key]; ok {
			if _, ok := value.(string); !ok {
				return item, ErrRegistry
			}
		}
	}
	item.ID, _ = stringValue(raw, "id")
	sourceID, _ := stringValue(raw, "sourceId")
	item.Title, _ = stringValue(raw, "title")
	item.Title = truncate(cleanInline(item.Title), 200)
	item.Prompt, _ = stringValue(raw, "prompt")
	if sourceID != source.ID || !strings.HasPrefix(item.ID, source.ID+":") || len(item.ID) <= len(source.ID)+1 || utf8.RuneCountInString(item.ID) > 160 || item.Title == "" || item.Prompt == "" || len(item.Prompt) > 512<<10 {
		return item, ErrRegistry
	}
	item.Description, _ = stringValue(raw, "description")
	item.Description = truncate(cleanDisplay(item.Description), 700)
	item.Preview, _ = stringValue(raw, "coverUrl")
	item.Link, _ = stringValue(raw, "sourceUrl")
	if _, err := validURL(item.Preview, true); err != nil {
		return item, err
	}
	if _, err := validURL(item.Link, false); err != nil {
		return item, err
	}
	var err error
	item.Tags, err = stringList(raw["tags"], 24)
	if err != nil {
		return item, err
	}
	item.ReferenceImageURLs, err = stringList(raw["referenceImageUrls"], 12)
	if err != nil {
		return item, err
	}
	for _, reference := range item.ReferenceImageURLs {
		if _, err := validURL(reference, false); err != nil {
			return item, err
		}
	}
	if len(item.Tags) > 0 {
		item.Category = truncate(item.Tags[0], 80)
	}
	if len(item.Tags) > 1 {
		item.SubCategory = truncate(item.Tags[1], 80)
	}
	item.Author, _ = stringValue(raw, "author")
	item.Author = truncate(cleanInline(item.Author), 120)
	item.CreatedAt, _ = stringValue(raw, "createdAt")
	item.ImageMode, _ = stringValue(raw, "imageMode")
	item.ImageMode = imageMode(item.ImageMode)
	item.ImageModel, _ = stringValue(raw, "imageModel")
	item.ImageModel = truncate(cleanInline(item.ImageModel), 80)
	item.ImageSize, _ = stringValue(raw, "imageSize")
	item.ImageSize = truncate(cleanInline(item.ImageSize), 40)
	if value, present := raw["imageCount"]; present {
		n, ok := integer(value)
		if !ok || n < 1 {
			return item, ErrRegistry
		}
		item.ImageCount = &n
	}
	return item, nil
}

func snapshotDigest(state snapshot) string {
	value := map[string]any{"registry_sources": state.Registry.Sources, "registry_revision": state.Registry.Revision, "registry_generated_at": state.Registry.GeneratedAt, "items_by_source": state.Items}
	data, _ := canonicalJSON(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
