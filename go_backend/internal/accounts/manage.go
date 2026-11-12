package accounts

import (
	"chatgpt2api/go_backend/ent"
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

type Filter struct {
	Page     int
	PageSize int
	Keyword  string `json:"keyword"`
	Status   string `json:"status"`
	GroupID  string `json:"group_id"`
}
type Selection struct {
	Mode       string   `json:"mode"`
	AccountIDs []string `json:"account_ids"`
	Excluded   []string `json:"excluded_account_ids"`
	Keyword    string   `json:"keyword"`
	Status     string   `json:"status"`
	GroupID    string   `json:"group_id"`
}
type Command struct {
	AccountIDs   []string   `json:"account_ids"`
	AccessTokens []string   `json:"access_tokens"`
	Tokens       []string   `json:"tokens"`
	Selection    *Selection `json:"selection"`
	Revision     string     `json:"revision"`
	Status       string     `json:"status"`
	Operation    string     `json:"operation"`
	GroupID      string     `json:"group_id"`
	Format       string     `json:"format"`
	Remove       bool       `json:"remove"`
}

func matches(m map[string]any, f Filter) bool {
	if f.Status != "" && f.Status != "all" && f.Status != category(m) && f.Status != text(m["status"]) {
		return false
	}
	group := text(m["group_id"])
	if f.GroupID == "__ungrouped__" && group != "" {
		return false
	}
	if f.GroupID != "" && f.GroupID != "all" && f.GroupID != "__ungrouped__" && group != f.GroupID {
		return false
	}
	needle := strings.ToLower(strings.TrimSpace(f.Keyword))
	if needle == "" {
		return true
	}
	for _, key := range []string{"management_id", "access_token", "email", "user_id", "type", "source_type", "status", "proxy", "group_id"} {
		if strings.Contains(strings.ToLower(text(m[key])), needle) {
			return true
		}
	}
	return false
}
func targets(items []record, cmd Command, defaultAll bool) ([]record, []string, error) {
	if len(cmd.AccountIDs)+len(cmd.AccessTokens)+len(cmd.Tokens) > MaxBatch {
		return nil, nil, invalid("每次最多选择 1000 条账号")
	}
	if cmd.Selection != nil && len(cmd.Selection.AccountIDs)+len(cmd.Selection.Excluded) > MaxBatch {
		return nil, nil, invalid("选择或排除列表过大")
	}
	mode := "explicit"
	selection := cmd.Selection
	if selection != nil {
		mode = selection.Mode
		if mode == "" {
			mode = "explicit"
		}
	} else if defaultAll && len(cmd.AccountIDs)+len(cmd.AccessTokens)+len(cmd.Tokens) == 0 {
		mode = "all"
	}
	if mode != "all" && mode != "filter" && mode != "explicit" {
		return nil, nil, invalid("选择模式无效")
	}
	ids := append([]string{}, cmd.AccountIDs...)
	tokenSet := map[string]bool{}
	for _, token := range append(append([]string{}, cmd.AccessTokens...), cmd.Tokens...) {
		tokenSet[strings.TrimSpace(token)] = true
	}
	excludes := map[string]bool{}
	filter := Filter{}
	if selection != nil {
		ids = append(ids, selection.AccountIDs...)
		for _, id := range selection.Excluded {
			excludes[strings.TrimSpace(id)] = true
		}
		filter = Filter{Keyword: selection.Keyword, Status: selection.Status, GroupID: selection.GroupID}
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	found := map[string]bool{}
	result := []record{}
	for _, rec := range items {
		id := accountID(rec.data)
		selectIt := wanted[id] || tokenSet[text(rec.data["access_token"])]
		if mode == "all" {
			selectIt = !excludes[id]
		}
		if mode == "filter" {
			selectIt = matches(rec.data, filter) && !excludes[id]
		}
		if selectIt {
			result = append(result, rec)
			found[id] = true
		}
	}
	missing := []string{}
	for id := range wanted {
		if !found[id] {
			if !validID.MatchString(id) {
				return nil, nil, invalid("账号 ID 格式无效")
			}
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(result) > MaxBatch {
		return nil, nil, invalid("每次最多处理 1000 条账号，请缩小选择范围")
	}
	return result, missing, nil
}
func loadGroups(ctx context.Context, q queryer) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, "SELECT data FROM account_group_configuration WHERE id=1")
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	groups := []map[string]any{}
	if rows.Next() {
		var raw []byte
		var payload map[string]json.RawMessage
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &payload) != nil {
			return nil, ErrStorage
		}
		if data := payload["account_groups"]; len(data) > 0 && decodeJSON(data, &groups) != nil {
			return nil, ErrStorage
		}
	}
	if rows.Err() != nil {
		return nil, ErrStorage
	}
	return groups, nil
}
func hasGroup(groups []map[string]any, id string) bool {
	if id == "" {
		return true
	}
	for _, group := range groups {
		if text(group["id"]) == id {
			return true
		}
	}
	return false
}
func (r *Repository) List(parent context.Context, f Filter) (map[string]any, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 500
	}
	if f.PageSize > 500 {
		return nil, invalid("每页最多 500 条")
	}
	items, err := r.snapshot(parent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	groups, err := loadGroups(ctx, r.client)
	if err != nil {
		return nil, err
	}
	filtered := []record{}
	for _, rec := range items {
		if matches(rec.data, f) {
			filtered = append(filtered, rec)
		}
	}
	rows := []any{}
	start := (f.Page - 1) * f.PageSize
	if start < 0 {
		start = len(filtered)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := start; i < len(filtered) && i < start+f.PageSize; i++ {
		rec := filtered[i]
		rows = append(rows, project(rec.data, groups, r.inflight[accountID(rec.data)]))
	}
	return map[string]any{"items": rows, "total": len(filtered), "all_total": len(items), "page": f.Page, "page_size": f.PageSize}, nil
}
func (r *Repository) raw(parent context.Context, id string) (record, error) {
	items, err := r.snapshot(parent)
	if err != nil {
		return record{}, err
	}
	for _, rec := range items {
		if accountID(rec.data) == id {
			return rec, nil
		}
	}
	return record{}, ErrNotFound
}
func (r *Repository) Detail(ctx context.Context, id string) (map[string]any, error) {
	rec, err := r.raw(ctx, id)
	if err != nil {
		return nil, err
	}
	groups, err := loadGroups(ctx, r.client)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	inflight := r.inflight[id]
	r.mu.Unlock()
	out := project(rec.data, groups, inflight)
	proxy, _, _, _ := proxyProjection(text(rec.data["proxy"]))
	out["configuration"] = map[string]any{"type": text(rec.data["type"]), "source_type": first(rec.data["source_type"], "web"), "quota": max(0, number(rec.data["quota"])), "proxy": proxy, "group_id": text(rec.data["group_id"])}
	out["diagnostics"] = map[string]any{"remote_check_error": safeDiagnostic(rec.data["last_remote_check_error"], rec.data), "refresh_error": safeDiagnostic(rec.data["last_refresh_error"], rec.data), "token_refresh_error": safeDiagnostic(rec.data["last_token_refresh_error"], rec.data), "last_invalid_at": timestamp(rec.data["last_invalid_at"]), "last_refresh_error_at": timestamp(rec.data["last_refresh_error_at"]), "last_token_refresh_at": timestamp(rec.data["last_token_refresh_at"])}
	return out, nil
}
func (r *Repository) Token(ctx context.Context, id, field string) (string, error) {
	if field != "access_token" && field != "refresh_token" {
		return "", ErrNotFound
	}
	rec, err := r.raw(ctx, id)
	if err != nil {
		return "", err
	}
	value := text(rec.data[field])
	if value == "" {
		return "", ErrNotFound
	}
	return value, nil
}
func (r *Repository) Preview(ctx context.Context, selection Selection) (map[string]any, error) {
	items, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	selected, missing, err := targets(items, Command{Selection: &selection}, false)
	if err != nil {
		return nil, err
	}
	all := selection
	all.Excluded = nil
	matching, _, err := targets(items, Command{Selection: &all}, false)
	if err != nil {
		return nil, err
	}
	exclude := []string{}
	if selection.Mode != "explicit" {
		for _, rec := range matching {
			for _, id := range selection.Excluded {
				if id == accountID(rec.data) {
					exclude = append(exclude, id)
					break
				}
			}
		}
	}
	op := emptyOperation()
	for _, id := range missing {
		operationError(&op, id, "account_not_found", "账号不存在", "select_account")
	}
	return map[string]any{"matching_count": len(matching), "selected_count": len(selected), "excluded_account_ids": exclude, "errors": op.Errors}, nil
}
func (r *Repository) Batch(parent context.Context, cmd Command, action string) (Operation, error) {
	release, err := r.processingSlot(parent)
	if err != nil {
		return emptyOperation(), err
	}
	defer release()
	return r.mutate(parent, cmd.Revision, func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
		selected, missing, err := targets(items, cmd, false)
		if err != nil {
			return false, err
		}
		if len(selected) == 0 && len(missing) == 0 {
			return false, invalid("请选择有效账号")
		}
		for _, id := range missing {
			operationError(result, id, "account_not_found", "账号不存在", action)
		}
		if action == "bind_account_group" {
			groups, err := loadGroups(ctx, tx)
			if err != nil {
				return false, err
			}
			if !hasGroup(groups, cmd.GroupID) {
				return false, ErrNotFound
			}
		}
		for _, rec := range selected {
			id := accountID(rec.data)
			if action == "delete_account" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", rec.row); err != nil {
					return false, ErrStorage
				}
				result.Removed++
				result.RemovedIDs = append(result.RemovedIDs, id)
				event(result, id, action, "success", "账号已删除")
				continue
			}
			if action == "bind_account_group" {
				rec.data["group_id"] = cmd.GroupID
			} else {
				switch cmd.Status {
				case "正常", "限流", "异常", "禁用":
					rec.data["status"] = cmd.Status
				default:
					return false, invalid("账号状态无效")
				}
				delete(rec.data, "enabled")
				if cmd.Operation == "reset" {
					for _, key := range []string{"success", "fail", "invalid_count"} {
						rec.data[key] = 0
					}
					for _, key := range []string{"last_remote_check_error", "last_refresh_error", "last_token_refresh_error", "refresh_token_invalid_at"} {
						rec.data[key] = nil
					}
					rec.data["last_remote_check_result"] = nil
				}
				if cmd.Status == "限流" {
					rec.data["quota"] = 0
					rec.data["image_quota_unknown"] = false
				}
			}
			if err := save(ctx, tx, rec); err != nil {
				return false, err
			}
			result.Updated++
			result.UpdatedIDs = append(result.UpdatedIDs, id)
			event(result, id, action, "success", "账号已更新")
		}
		return len(selected) > 0, nil
	})
}
func (r *Repository) Update(parent context.Context, body map[string]any) (Operation, error) {
	allowed := map[string]bool{"id": true, "access_token": true, "refresh_token": true, "type": true, "source_type": true, "quota": true, "proxy": true, "group_id": true, "revision": true}
	for key := range body {
		if !allowed[key] {
			return emptyOperation(), invalid("账号更新包含不支持的字段")
		}
		if key != "quota" && body[key] != nil {
			if _, ok := body[key].(string); !ok {
				return emptyOperation(), invalid("账号更新字段类型无效")
			}
		}
	}
	id := text(body["id"])
	release, err := r.processingSlot(parent)
	if err != nil {
		return emptyOperation(), err
	}
	defer release()
	return r.mutate(parent, text(body["revision"]), func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
		var selected *record
		for i := range items {
			if (id != "" && accountID(items[i].data) == id) || (id == "" && text(items[i].data["access_token"]) == text(body["access_token"])) {
				selected = &items[i]
				break
			}
		}
		if selected == nil {
			return false, ErrNotFound
		}
		rec := *selected
		changed := false
		for key, value := range body {
			if key == "id" || key == "revision" || value == nil {
				continue
			}
			if (key == "access_token" || key == "refresh_token") && (id == "" || text(value) == "") {
				continue
			}
			if key == "access_token" || key == "refresh_token" {
				if !validateToken(text(value)) {
					return false, invalid("凭据无效或超过 64 KiB")
				}
				if key == "access_token" {
					for _, other := range items {
						if other.row != rec.row && text(other.data["access_token"]) == text(value) {
							return false, ErrConflict
						}
					}
				}
				if key == "refresh_token" && text(rec.data[key]) != text(value) {
					rec.data["refresh_token_invalid_at"] = nil
				}
				if key == "access_token" && text(rec.data[key]) != text(value) {
					for _, field := range []string{"last_remote_check_result", "last_remote_check_error", "last_token_refresh_error"} {
						rec.data[field] = nil
					}
				}
			}
			if key == "proxy" {
				if text(value) == "custom" {
					continue
				}
				if err := validateReference(text(value)); err != nil {
					return false, err
				}
				if err := validateProxyGroup(ctx, tx, text(value)); err != nil {
					return false, err
				}
			}
			if key == "group_id" {
				groups, err := loadGroups(ctx, tx)
				if err != nil {
					return false, err
				}
				if !hasGroup(groups, text(value)) {
					return false, ErrNotFound
				}
			}
			if key == "source_type" && text(value) != "web" && text(value) != "codex" {
				return false, invalid("账号来源无效")
			}
			if key == "quota" {
				n, ok := value.(float64)
				if numeric, isNumber := value.(json.Number); isNumber {
					var err error
					n, err = numeric.Float64()
					ok = err == nil
				}
				if !ok || n < 0 || n > 100000000 || n != float64(int(n)) {
					return false, invalid("额度必须为非负整数")
				}
			}
			rec.data[key] = value
			changed = true
		}
		if !changed {
			return false, invalid("没有可保存的改动")
		}
		rec.data = normalize(rec.data)
		if err := save(ctx, tx, rec); err != nil {
			return false, err
		}
		result.Updated = 1
		result.UpdatedIDs = append(result.UpdatedIDs, accountID(rec.data))
		result.Item = project(rec.data, nil, 0)
		result.Items = append(result.Items, result.Item)
		event(result, accountID(rec.data), "update_account", "success", "账号已更新")
		return true, nil
	})
}
func (r *Repository) Export(ctx context.Context, cmd Command) ([]map[string]any, error) {
	items, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	selected, missing, err := targets(items, cmd, true)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 || len(selected) == 0 {
		return nil, ErrNotFound
	}
	out := []map[string]any{}
	for _, rec := range selected {
		out = append(out, clone(rec.data))
	}
	return out, nil
}
func validateProxyGroup(ctx context.Context, q queryer, reference string) error {
	if !strings.HasPrefix(reference, "group:") {
		return nil
	}
	rows, err := q.QueryContext(ctx, "SELECT data FROM proxy_configuration WHERE id=1")
	if err != nil {
		return ErrStorage
	}
	defer rows.Close()
	if rows.Next() {
		var raw []byte
		var data map[string]any
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &data) != nil {
			return ErrStorage
		}
		if groups, ok := data["proxy_groups"].([]any); ok {
			for _, raw := range groups {
				if group, ok := raw.(map[string]any); ok && text(group["id"]) == strings.TrimPrefix(reference, "group:") {
					return nil
				}
			}
		}
	}
	return invalid("引用的代理组不存在")
}
func (r *Repository) Groups(parent context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	groups, err := loadGroups(ctx, r.client)
	if err != nil {
		return nil, err
	}
	items, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := []any{}
	for _, group := range groups {
		projected := clone(group)
		ref, mode, gid, label := proxyProjection(text(group["proxy"]))
		projected["proxy"] = ref
		projected["proxy_mode"] = mode
		projected["proxy_group_id"] = gid
		projected["proxy_label"] = label
		count := 0
		for _, rec := range items {
			if text(rec.data["group_id"]) == text(group["id"]) {
				count++
			}
		}
		projected["account_count"] = count
		result = append(result, projected)
	}
	return map[string]any{"groups": result, "proxy_groups": []any{}}, nil
}

var groupIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-\p{Han}]{1,128}$`)

func (r *Repository) SaveGroup(parent context.Context, body map[string]any, deleteID string) (map[string]any, error) {
	for key, value := range body {
		switch key {
		case "id", "name", "notes", "proxy", "proxy_group_id", "proxy_mode", "proxy_label":
			if _, ok := value.(string); !ok || len(text(value)) > 4096 {
				return nil, invalid("账号组字段类型或长度无效")
			}
		case "enabled", "create_only":
			if _, ok := value.(bool); !ok {
				return nil, invalid("账号组启用/创建标志必须为布尔值")
			}
		case "account_count":
		default:
			return nil, invalid("账号组包含不支持的字段")
		}
	}
	id := first(body["id"], body["name"], deleteID)
	if !groupIDPattern.MatchString(id) {
		return nil, invalid("账号组 ID 格式无效")
	}
	release, err := r.processingSlot(parent)
	if err != nil {
		return nil, err
	}
	defer release()
	var saved map[string]any
	operation, err := r.mutate(parent, "", func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
		groups, err := loadGroups(ctx, tx)
		if err != nil {
			return false, err
		}
		exists := hasGroup(groups, id)
		if deleteID != "" && !exists {
			return false, ErrNotFound
		}
		if deleteID == "" && exists && flag(body["create_only"], false) {
			return false, ErrConflict
		}
		next := []map[string]any{}
		var current map[string]any
		for _, group := range groups {
			if text(group["id"]) != id {
				next = append(next, group)
			} else {
				current = group
			}
		}
		if deleteID == "" {
			proxy := text(body["proxy"])
			if proxy == "custom" && current != nil {
				proxy = text(current["proxy"])
			}
			if proxy == "" && text(body["proxy_group_id"]) != "" {
				proxy = "group:" + text(body["proxy_group_id"])
			}
			if err := validateReference(proxy); err != nil {
				return false, err
			}
			if err := validateProxyGroup(ctx, tx, proxy); err != nil {
				return false, err
			}
			saved = clone(current)
			saved["id"] = id
			saved["name"] = first(body["name"], saved["name"], id)
			if _, ok := body["proxy"]; ok || body["proxy_group_id"] != nil || current == nil {
				saved["proxy"] = proxy
			}
			if value, ok := body["enabled"]; ok {
				saved["enabled"] = value
			} else if current == nil {
				saved["enabled"] = true
			}
			if value, ok := body["notes"]; ok {
				saved["notes"] = value
			}
			next = append(next, saved)
		} else {
			for _, rec := range items {
				if text(rec.data["group_id"]) == id {
					rec.data["group_id"] = ""
					if err := save(ctx, tx, rec); err != nil {
						return false, err
					}
					result.UpdatedIDs = append(result.UpdatedIDs, accountID(rec.data))
				}
			}
		}
		data, _ := json.Marshal(map[string]any{"account_groups": next})
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_configuration(id,data,updated_at) VALUES(1,$1,now()) ON CONFLICT(id) DO UPDATE SET data=jsonb_set(account_group_configuration.data,'{account_groups}',$2::jsonb),updated_at=now()`, string(data), mustJSON(next)); err != nil {
			return false, ErrStorage
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	view, err := r.Groups(parent)
	if err != nil {
		return nil, err
	}
	if deleteID != "" {
		view["deleted"] = id
		view["updated_ids"] = operation.UpdatedIDs
		view["removed_ids"] = []string{}
		view["errors"] = []any{}
	} else {
		for _, item := range view["groups"].([]any) {
			if text(item.(map[string]any)["id"]) == id {
				view["group"] = item
			}
		}
	}
	return view, nil
}
func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
