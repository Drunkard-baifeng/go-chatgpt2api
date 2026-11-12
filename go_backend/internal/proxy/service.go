// Package proxy 拥有默认出口、代理组、选择策略与安全业务投影。
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"chatgpt2api/go_backend/internal/callrecord"

	"golang.org/x/net/idna"
)

var (
	ErrInput       = errors.New("代理参数无效，请检查地址、代理组和节点配置")
	ErrNotFound    = errors.New("代理组或节点不存在")
	ErrInUse       = errors.New("代理组仍被默认出口、账号或账号组引用")
	ErrUnavailable = errors.New("所选代理出口不可用，请检查代理组和节点")
	ErrStorage     = errors.New("代理存储暂不可用")
	ErrBusy        = errors.New("所选代理图片容量已满，请稍后重试")
)

type Reference struct {
	Mode           string `json:"mode"`
	GroupID        string `json:"group_id"`
	URL            string `json:"url"`
	HasCredentials bool   `json:"has_credentials"`
}
type DefaultsRequest struct {
	DefaultReference  Reference  `json:"default_reference"`
	FallbackReference *Reference `json:"fallback_reference"`
}
type Service struct {
	db            *sql.DB
	timeout       time.Duration
	mu            sync.Mutex
	rotation      map[string]uint64
	imageInflight map[string]int
	health        map[string]Health
	probeTarget   string
	probeSlots    chan struct{}
}
type Health struct {
	State     string  `json:"state"`
	CheckedAt *string `json:"checked_at"`
	LatencyMS *int64  `json:"latency_ms"`
	Error     *string `json:"error"`
	Status    int     `json:"status"`
	ExitIP    string  `json:"exit_ip"`
	Country   string  `json:"country"`
}

func New(db *sql.DB, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Service{db: db, timeout: timeout, rotation: map[string]uint64{}, imageInflight: map[string]int{}, health: map[string]Health{}, probeTarget: "https://chatgpt.com/cdn-cgi/trace", probeSlots: make(chan struct{}, 4)}
}
func (s *Service) load(ctx context.Context) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM proxy_configuration WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	return decode(data)
}
func decode(data []byte) (map[string]any, error) {
	v := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil || v == nil {
		return nil, ErrStorage
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return nil, ErrStorage
	}
	return v, nil
}
func (s *Service) mutate(ctx context.Context, fn func(context.Context, *sql.Tx, map[string]any) error) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('proxy_configuration'))"); err != nil {
		return nil, ErrStorage
	}
	raw := map[string]any{}
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM proxy_configuration WHERE id=1 FOR UPDATE").Scan(&data)
	if err == nil {
		raw, err = decode(data)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	if err = fn(ctx, tx, raw); err != nil {
		return nil, err
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return nil, ErrInput
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO proxy_configuration(id,data,updated_at) VALUES(1,$1,now()) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", data); err != nil {
		return nil, ErrStorage
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrStorage
	}
	return raw, nil
}
func (s *Service) View(ctx context.Context) (map[string]any, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	references, err := s.references(ctx, s.db, raw)
	if err != nil {
		return nil, err
	}
	return s.project(raw, references), nil
}
func (s *Service) project(raw map[string]any, references map[string][]string) map[string]any {
	groups := []map[string]any{}
	for _, group := range dicts(raw["proxy_groups"]) {
		groups = append(groups, s.groupView(group, references[text(group["id"])]))
	}
	sort.Slice(groups, func(i, j int) bool { return text(groups[i]["name"]) < text(groups[j]["name"]) })
	def := parseReference(text(raw["proxy"]), true)
	fallback := parseReference(text(raw["fallback_proxy"]), false)
	return map[string]any{"schema_version": 1, "generated_at": time.Now().UTC().Format(time.RFC3339Nano), "revision": revision(raw), "default_reference": publicReference(def), "fallback_reference": publicReference(fallback), "effective_default": s.effective(raw, def), "effective_fallback": s.effective(raw, fallback), "groups": groups}
}
func (s *Service) groupView(raw map[string]any, references []string) map[string]any {
	if references == nil {
		references = []string{}
	}
	nodes := []map[string]any{}
	id := text(raw["id"])
	for i, node := range dicts(raw["nodes"]) {
		nodeID := nodeID(node, i)
		rawURL := text(node["url"])
		limit, _ := concurrency(node["image_concurrency_limit"], 30)
		nodes = append(nodes, map[string]any{"id": nodeID, "name": or(text(node["name"]), nodeID), "url": publicURL(rawURL), "has_credentials": hasCredentials(rawURL), "enabled": enabled(node), "image_concurrency_limit": limit, "notes": text(node["notes"]), "health": s.nodeHealth(rawURL)})
	}
	return map[string]any{"id": id, "name": or(text(raw["name"]), id), "strategy": or(text(raw["strategy"]), "request_random"), "rotation_interval_minutes": number(raw["rotation_interval_minutes"]), "enabled": enabled(raw), "notes": text(raw["notes"]), "nodes": nodes, "reference_text": "group:" + id, "health": s.groupHealth(raw), "can_delete": len(references) == 0, "references": references}
}
func (s *Service) nodeHealth(rawURL string) Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.health[rawURL]; ok {
		return h
	}
	return Health{State: "unknown"}
}
func (s *Service) groupHealth(group map[string]any) Health {
	state := "unknown"
	found := false
	for _, node := range dicts(group["nodes"]) {
		if !enabled(node) {
			continue
		}
		h := s.nodeHealth(text(node["url"]))
		if h.State == "healthy" {
			return h
		}
		if h.State == "unhealthy" {
			found = true
			state = "unhealthy"
		} else {
			return Health{State: "unknown"}
		}
	}
	if !found {
		return Health{State: "unknown"}
	}
	return Health{State: state}
}
func parseReference(raw string, emptyDirect bool) *Reference {
	if raw == "" {
		if emptyDirect {
			return &Reference{Mode: "direct"}
		}
		return nil
	}
	if raw == "direct" {
		return &Reference{Mode: "direct"}
	}
	if strings.HasPrefix(raw, "group:") {
		return &Reference{Mode: "group", GroupID: strings.TrimPrefix(raw, "group:")}
	}
	return &Reference{Mode: "custom", URL: raw}
}
func publicReference(ref *Reference) *Reference {
	if ref == nil {
		return nil
	}
	copy := *ref
	copy.HasCredentials = hasCredentials(copy.URL)
	copy.URL = publicURL(copy.URL)
	return &copy
}
func (s *Service) effective(raw map[string]any, ref *Reference) map[string]any {
	source, label := "disabled", "未配置备用出口"
	available, configured, hasProxy := false, false, false
	groupID := ""
	if ref != nil {
		source = ref.Mode
		configured = true
		groupID = ref.GroupID
		switch ref.Mode {
		case "direct":
			label = "强制直连"
			available = true
		case "custom":
			label = publicURL(ref.URL)
			_, err := NormalizeURL(ref.URL)
			available = err == nil
			hasProxy = available
		case "group":
			label = "代理组：" + groupID
			group := findGroup(raw, groupID)
			if group != nil {
				label = "代理组：" + or(text(group["name"]), groupID)
				available = enabled(group) && len(availableNodes(group)) > 0
				hasProxy = available
			}
		}
	}
	return map[string]any{"source": source, "label": label, "configured": configured, "available": available, "has_proxy": hasProxy, "group_id": groupID}
}
func (s *Service) SaveDefaults(ctx context.Context, input DefaultsRequest) (map[string]any, error) {
	raw, err := s.mutate(ctx, func(_ context.Context, _ *sql.Tx, raw map[string]any) error {
		value, err := serializeReference(input.DefaultReference, text(raw["proxy"]), raw)
		if err != nil {
			return err
		}
		fallback := ""
		if input.FallbackReference != nil {
			fallback, err = serializeReference(*input.FallbackReference, text(raw["fallback_proxy"]), raw)
			if err != nil {
				return err
			}
		}
		raw["proxy"] = value
		raw["fallback_proxy"] = fallback
		return nil
	})
	if err != nil {
		return nil, err
	}
	view := s.project(raw, nil)
	delete(view, "groups")
	delete(view, "schema_version")
	delete(view, "generated_at")
	return view, nil
}
func serializeReference(ref Reference, previous string, raw map[string]any) (string, error) {
	switch ref.Mode {
	case "direct":
		if ref.GroupID != "" || ref.URL != "" {
			return "", ErrInput
		}
		return "direct", nil
	case "group":
		if ref.GroupID == "" || ref.URL != "" {
			return "", ErrInput
		}
		group := findGroup(raw, ref.GroupID)
		if group == nil || !enabled(group) || len(availableNodes(group)) == 0 {
			return "", ErrUnavailable
		}
		return "group:" + ref.GroupID, nil
	case "custom":
		if ref.GroupID != "" {
			return "", ErrInput
		}
		if previous != "" && ref.URL == publicURL(previous) {
			ref.URL = previous
		}
		return NormalizeURL(ref.URL)
	default:
		return "", ErrInput
	}
}
func (s *Service) SaveGroup(ctx context.Context, input map[string]any) (map[string]any, error) {
	if !only(input, "id", "name", "strategy", "rotation_interval_minutes", "enabled", "notes", "nodes", "create_only") {
		return nil, ErrInput
	}
	var groupID string
	raw, err := s.mutate(ctx, func(_ context.Context, _ *sql.Tx, raw map[string]any) error {
		requested := strings.TrimPrefix(text(input["id"]), "group:")
		existing := findGroup(raw, requested)
		createOnly, _ := input["create_only"].(bool)
		if createOnly && existing != nil {
			return ErrInput
		}
		groupID = requested
		if existing == nil {
			groupID = slug(or(requested, text(input["name"])))
		}
		if groupID == "" {
			return ErrInput
		}
		if existing == nil && findGroup(raw, groupID) != nil {
			return ErrInput
		}
		group := map[string]any{"id": groupID, "name": groupID, "strategy": "request_random", "rotation_interval_minutes": float64(0), "enabled": true, "notes": ""}
		if existing != nil {
			group = clone(existing).(map[string]any)
		}
		for _, key := range []string{"name", "strategy", "notes"} {
			if value, ok := input[key]; ok && value != nil {
				str, ok := value.(string)
				if !ok || len(str) > 10000 {
					return ErrInput
				}
				group[key] = strings.TrimSpace(str)
			}
		}
		if text(group["name"]) == "" {
			group["name"] = groupID
		}
		strategy := text(group["strategy"])
		if strategy != "request_random" && strategy != "time_window" && strategy != "round_robin" {
			return ErrInput
		}
		if v, ok := input["enabled"]; ok && v != nil {
			b, ok := v.(bool)
			if !ok {
				return ErrInput
			}
			group["enabled"] = b
		}
		if v, ok := input["rotation_interval_minutes"]; ok && v != nil {
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1440 {
				return ErrInput
			}
			group["rotation_interval_minutes"] = n
		}
		if v, ok := input["nodes"]; ok && v != nil {
			values, ok := v.([]any)
			if !ok || len(values) > 10000 {
				return ErrInput
			}
			nodes := []any{}
			seenID, seenURL := map[string]bool{}, map[string]bool{}
			oldNodes := dicts(group["nodes"])
			for i, value := range values {
				node, ok := value.(map[string]any)
				if !ok || !only(node, "id", "name", "url", "enabled", "image_concurrency_limit", "notes") {
					return ErrInput
				}
				id := slug(or(text(node["id"]), or(text(node["name"]), fmt.Sprintf("node-%d", i+1))))
				var old map[string]any
				for j, n := range oldNodes {
					if nodeID(n, j) == id {
						old = n
					}
				}
				rawURL := text(node["url"])
				if old != nil && (rawURL == "" || rawURL == publicURL(text(old["url"]))) {
					rawURL = text(old["url"])
				}
				normalized, err := NormalizeURL(rawURL)
				if err != nil {
					return err
				}
				if seenID[id] || seenURL[normalized] {
					return ErrInput
				}
				seenID[id] = true
				seenURL[normalized] = true
				stored := map[string]any{}
				if old != nil {
					stored = clone(old).(map[string]any)
				}
				fallbackLimit := 30
				if old != nil {
					fallbackLimit, _ = concurrency(old["image_concurrency_limit"], 30)
				}
				limit, err := concurrency(node["image_concurrency_limit"], fallbackLimit)
				if err != nil {
					return err
				}
				stored["id"] = id
				stored["url"] = normalized
				stored["name"] = or(text(node["name"]), id)
				stored["enabled"] = enabled(node)
				stored["notes"] = text(node["notes"])
				stored["image_concurrency_limit"] = limit
				nodes = append(nodes, stored)
			}
			group["nodes"] = nodes
		}
		if len(dicts(group["nodes"])) == 0 {
			return ErrInput
		}
		next := []any{}
		for _, g := range dicts(raw["proxy_groups"]) {
			if text(g["id"]) != groupID {
				next = append(next, g)
			}
		}
		next = append(next, group)
		raw["proxy_groups"] = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	references, err := s.references(ctx, s.db, raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"group": s.groupView(findGroup(raw, groupID), references[groupID]), "revision": revision(raw)}, nil
}
func (s *Service) DeleteGroup(ctx context.Context, id string) (map[string]any, error) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "group:")
	raw, err := s.mutate(ctx, func(ctx context.Context, tx *sql.Tx, raw map[string]any) error {
		if findGroup(raw, id) == nil {
			return ErrNotFound
		}
		refs, err := s.references(ctx, tx, raw)
		if err != nil {
			return err
		}
		if len(refs[id]) > 0 {
			return ErrInUse
		}
		next := []any{}
		for _, group := range dicts(raw["proxy_groups"]) {
			if text(group["id"]) != id {
				next = append(next, group)
			}
		}
		raw["proxy_groups"] = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted_id": id, "revision": revision(raw)}, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) references(ctx context.Context, q queryer, raw map[string]any) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	refs := map[string][]string{}
	add := func(reference, label string) {
		if strings.HasPrefix(reference, "group:") {
			id := strings.TrimPrefix(reference, "group:")
			refs[id] = append(refs[id], label)
		}
	}
	add(text(raw["proxy"]), "默认出口")
	add(text(raw["fallback_proxy"]), "备用出口")
	rows, err := q.QueryContext(ctx, "SELECT data FROM accounts")
	if err != nil {
		return nil, ErrStorage
	}
	count := 0
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			rows.Close()
			return nil, ErrStorage
		}
		v, err := decode(data)
		if err != nil {
			rows.Close()
			return nil, err
		}
		count++
		add(assignment(v), fmt.Sprintf("账号 %d", count))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, ErrStorage
	}
	rows, err = q.QueryContext(ctx, "SELECT data FROM account_group_configuration WHERE id=1")
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			return nil, ErrStorage
		}
		v, err := decode(data)
		if err != nil {
			return nil, err
		}
		for i, group := range dicts(v["account_groups"]) {
			add(assignment(group), fmt.Sprintf("账号组 %d", i+1))
		}
	}
	if rows.Err() != nil {
		return nil, ErrStorage
	}
	return refs, nil
}

// Resolve 返回内部可用、保留凭据的实际出口；任何显式无效引用都不会静默降级。
func (s *Service) Resolve(ctx context.Context, account, group map[string]any) (string, error) {
	raw, err := s.load(ctx)
	if err != nil {
		return "", err
	}
	return s.resolve(raw, account, group)
}

// AcquireImage 为实际出口原子预留图片容量。标识是含认证信息的规范化 URL；
// 不同代理组引用同一出口时共用计数，采用所有已启用引用中最小的非零限制。
// 0 不施加限制；仍记录活动请求，使运行中收紧配置立即约束后续准入。
func (s *Service) AcquireImage(ctx context.Context, account, group map[string]any) (actualResult string, release func(), err error) {
	finish := callrecord.Span(ctx, "egress_acquire", "egress_acquire_ms")
	defer func() { finish(err) }()
	raw, err := s.load(ctx)
	if err != nil {
		return "", nil, err
	}
	reference := routeReference(raw, account, group)
	var selectedGroup map[string]any
	actuals := []string{""}
	if strings.HasPrefix(reference, "group:") {
		selectedGroup = findGroup(raw, strings.TrimPrefix(reference, "group:"))
		if selectedGroup == nil || !enabled(selectedGroup) {
			return "", nil, ErrUnavailable
		}
		actuals = nil
		for _, node := range availableNodes(selectedGroup) {
			actual, _ := NormalizeURL(text(node["url"]))
			actuals = append(actuals, actual)
		}
		if len(actuals) == 0 {
			return "", nil, ErrUnavailable
		}
	} else if reference != "" && reference != "direct" && reference != "global" {
		actual, err := NormalizeURL(reference)
		if err != nil {
			return "", nil, err
		}
		actuals[0] = actual
	}
	limits := map[string]int{}
	for _, actual := range actuals {
		limits[actual] = 0
	}
	for _, candidate := range dicts(raw["proxy_groups"]) {
		if !enabled(candidate) {
			continue
		}
		for _, node := range availableNodes(candidate) {
			normalized, _ := NormalizeURL(text(node["url"]))
			limit, selected := limits[normalized]
			if !selected {
				continue
			}
			n, e := concurrency(node["image_concurrency_limit"], 30)
			if e != nil {
				return "", nil, ErrUnavailable
			}
			if n > 0 && (limit == 0 || n < limit) {
				limits[normalized] = n
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	start := 0
	if selectedGroup != nil {
		start, err = s.nodeIndexLocked(selectedGroup, len(actuals))
		if err != nil {
			return "", nil, err
		}
	}
	actual := ""
	found := false
	for offset := 0; offset < len(actuals); offset++ {
		candidate := actuals[(start+offset)%len(actuals)]
		if limit := limits[candidate]; limit == 0 || s.imageInflight[candidate] < limit {
			actual, found = candidate, true
			break
		}
	}
	if !found {
		return "", nil, ErrBusy
	}
	s.imageInflight[actual]++
	var once sync.Once
	return actual, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.imageInflight[actual]--
			if s.imageInflight[actual] <= 0 {
				delete(s.imageInflight, actual)
			}
		})
	}, nil
}

// ActiveImageEgress 复用真实租约计数，仅返回数量，不暴露代理地址或认证信息。
func (s *Service) ActiveImageEgress() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.imageInflight)
}
func (s *Service) resolve(raw, account, group map[string]any) (string, error) {
	return s.resolveReference(raw, routeReference(raw, account, group))
}
func routeReference(raw, account, group map[string]any) string {
	reference := assignment(account)
	if reference == "" {
		reference = assignment(group)
	}
	if reference == "" {
		reference = text(raw["proxy"])
	}
	return reference
}
func (s *Service) ValidateReference(ctx context.Context, reference string) error {
	if strings.TrimSpace(reference) == "" || reference == "global" {
		return nil
	}
	raw, err := s.load(ctx)
	if err != nil {
		return err
	}
	_, err = s.resolveReference(raw, reference)
	return err
}
func (s *Service) resolveReference(raw map[string]any, reference string) (string, error) {
	if reference == "" || reference == "direct" || reference == "global" {
		return "", nil
	}
	if strings.HasPrefix(reference, "group:") {
		id := strings.TrimPrefix(reference, "group:")
		group := findGroup(raw, id)
		if group == nil || !enabled(group) {
			return "", ErrUnavailable
		}
		nodes := availableNodes(group)
		if len(nodes) == 0 {
			return "", ErrUnavailable
		}
		s.mu.Lock()
		index, err := s.nodeIndexLocked(group, len(nodes))
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		return NormalizeURL(text(nodes[index]["url"]))
	}
	return NormalizeURL(reference)
}
func (s *Service) nodeIndexLocked(group map[string]any, count int) (int, error) {
	switch text(group["strategy"]) {
	case "round_robin":
		id := text(group["id"])
		index := int(s.rotation[id] % uint64(count))
		s.rotation[id]++
		return index, nil
	case "time_window":
		minutes := number(group["rotation_interval_minutes"])
		if minutes <= 0 {
			minutes = 1
		}
		return int(time.Now().Unix()/int64(math.Max(1, minutes*60))) % count, nil
	default:
		n, err := rand.Int(rand.Reader, big.NewInt(int64(count)))
		if err != nil {
			return 0, ErrUnavailable
		}
		return int(n.Int64()), nil
	}
}
func assignment(item map[string]any) string {
	raw := text(item["proxy"])
	if raw == "global" {
		raw = ""
	}
	if raw == "" {
		if id := text(item["proxy_group_id"]); id != "" {
			raw = "group:" + strings.TrimPrefix(id, "group:")
		}
	}
	return raw
}
func availableNodes(group map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, node := range dicts(group["nodes"]) {
		if enabled(node) {
			if _, err := NormalizeURL(text(node["url"])); err == nil {
				out = append(out, node)
			}
		}
	}
	return out
}

// NormalizeURL 校验可信运维代理目标；允许内网代理，与用户图片下载 URL 使用不同策略。
func NormalizeURL(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n\x00#") {
		return "", ErrInput
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8192 || strings.IndexFunc(raw, unicode.IsSpace) >= 0 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", ErrInput
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrInput
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", ErrInput
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", ErrInput
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", ErrInput
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "%") {
		return "", ErrInput
	}
	if net.ParseIP(host) == nil {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" {
			return "", ErrInput
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}
func publicURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}
func hasCredentials(raw string) bool { u, err := url.Parse(raw); return err == nil && u.User != nil }
func clone(v any) any {
	switch value := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = clone(item)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = clone(item)
		}
		return result
	default:
		return v
	}
}
func revision(v map[string]any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
func dicts(v any) []map[string]any {
	result := []map[string]any{}
	if values, ok := v.([]any); ok {
		for _, value := range values {
			if m, ok := value.(map[string]any); ok {
				result = append(result, m)
			}
		}
	}
	return result
}
func text(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
func number(v any) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case json.Number:
		n, _ := value.Float64()
		return n
	default:
		return 0
	}
}
func enabled(v map[string]any) bool { return v["enabled"] != false }
func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func slug(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len([]rune(out)) > 64 {
		out = string([]rune(out)[:64])
	}
	return out
}
func nodeID(v map[string]any, i int) string {
	return or(text(v["id"]), or(text(v["name"]), fmt.Sprintf("node-%d", i+1)))
}
func findGroup(raw map[string]any, id string) map[string]any {
	for _, group := range dicts(raw["proxy_groups"]) {
		if text(group["id"]) == id {
			return group
		}
	}
	return nil
}
func only(v map[string]any, keys ...string) bool {
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range v {
		if !allowed[key] {
			return false
		}
	}
	return true
}
func concurrency(v any, fallback int) (int, error) {
	if v == nil {
		return fallback, nil
	}
	var n float64
	switch value := v.(type) {
	case float64:
		n = value
	case int:
		n = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, ErrInput
		}
		n = parsed
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, ErrInput
		}
		n = parsed
	default:
		return 0, ErrInput
	}
	if n < 0 || n > 10000 || math.Trunc(n) != n || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, ErrInput
	}
	return int(n), nil
}
