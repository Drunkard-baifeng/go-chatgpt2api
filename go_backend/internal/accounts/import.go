package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"

	"chatgpt2api/go_backend/ent"
)

const MaxControlBytes = 32 << 20
const MaxBatch = 1000

var ErrConflict = errors.New("数据已变更，请刷新后重试")
var ErrNotFound = errors.New("账号或账号组不存在")
var ErrBusy = errors.New("账号处理容量已满，请稍后重试")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func invalid(message string) error  { return &InputError{message} }

type ImportRequest struct {
	Tokens         []string         `json:"tokens"`
	Accounts       []map[string]any `json:"accounts"`
	CredentialText string           `json:"credential_text"`
	Sync           *bool            `json:"sync_after_import"`
	Refresh        *bool            `json:"refresh"`
	Restore        bool             `json:"restore"`
	ReturnItems    *bool            `json:"return_items"`
	TargetGroup    *string          `json:"target_group_id"`
	Revision       string           `json:"revision"`
}

func Decode(reader io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(reader, MaxControlBytes+1))
	if err != nil || len(data) > MaxControlBytes || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return invalid("请求必须是有效 JSON 对象且不超过 32 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if decoder.Decode(value) != nil {
		return invalid("请求格式无效或包含不支持的字段")
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return invalid("请求只能包含一个 JSON 对象")
	}
	return nil
}
func ParseImport(reader io.Reader) (ImportRequest, error) {
	var req ImportRequest
	err := Decode(reader, &req)
	return req, err
}
func (req ImportRequest) ShouldSync() bool {
	if req.Sync != nil {
		return *req.Sync
	}
	if req.Refresh != nil {
		return *req.Refresh
	}
	return true
}
func text(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
func number(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
		n, _ := x.Float64()
		return int(n)
	}
	return 0
}
func flag(v any, fallback bool) bool {
	if v == nil {
		return fallback
	}
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}
func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = cloneValue(value)
	}
	return out
}
func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return clone(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}
func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return ErrStorage
	}
	return nil
}
func first(values ...any) string {
	for _, v := range values {
		if s := text(v); s != "" {
			return s
		}
	}
	return ""
}
func digest(token string) string {
	value := sha256.Sum256([]byte(token))
	return hex.EncodeToString(value[:])
}
func accountID(m map[string]any) string {
	if id := text(m["management_id"]); validID.MatchString(id) {
		return id
	}
	if id := text(m["id"]); validID.MatchString(id) {
		return id
	}
	return "acct_" + digest(text(m["access_token"]))[:24]
}
func validateReference(value string) error {
	if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00#") {
		return invalid("代理格式无效或过长")
	}
	if value == "" || value == "direct" || value == "global" || strings.HasPrefix(value, "group:") {
		if len(value) > 255 {
			return invalid("代理引用过长")
		}
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
		return invalid("代理格式无效")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return invalid("代理协议不受支持")
}
func validateToken(token string) bool {
	return token != "" && len(token) <= 65536 && !strings.ContainsAny(token, "\r\n\x00")
}
func credentials(raw map[string]any) map[string]any {
	out := clone(raw)
	sources := []map[string]any{}
	for _, key := range []string{"credentials", "credential", "tokens", "auth"} {
		if nested, ok := raw[key].(map[string]any); ok {
			sources = append(sources, nested)
		}
	}
	sources = append(sources, raw)
	for target, aliases := range map[string][]string{"access_token": {"access_token", "accessToken", "token", "cookie"}, "refresh_token": {"refresh_token", "refreshToken"}, "id_token": {"id_token", "idToken"}} {
		for _, source := range sources {
			for _, alias := range aliases {
				if value := text(source[alias]); value != "" {
					out[target] = value
					goto nextField
				}
			}
		}
	nextField:
	}
	for _, key := range []string{"credentials", "credential", "tokens", "auth", "accessToken", "refreshToken", "idToken", "token", "cookie"} {
		delete(out, key)
	}
	return out
}
func (req ImportRequest) payloads() ([]map[string]any, error) {
	values := []map[string]any{}
	for _, raw := range req.Accounts {
		values = append(values, credentials(raw))
	}
	for index, line := range strings.Split(strings.TrimPrefix(req.CredentialText, "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) != 2 {
			return nil, invalid(fmt.Sprintf("第 %d 行格式错误，请使用 AT----RT", index+1))
		}
		at, rt := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if at == "" || rt == "" || strings.IndexFunc(at+rt, unicode.IsSpace) >= 0 {
			return nil, invalid(fmt.Sprintf("第 %d 行格式错误，AT 和 RT 均不能为空或包含空白字符", index+1))
		}
		values = append(values, map[string]any{"access_token": at, "refresh_token": rt})
	}
	for _, token := range req.Tokens {
		values = append(values, map[string]any{"access_token": strings.TrimSpace(token)})
	}
	if len(values) == 0 || len(values) > MaxBatch {
		return nil, invalid("每批需要 1 到 1000 条账号")
	}
	for i, value := range values {
		for _, key := range []string{"access_token", "refresh_token", "id_token", "proxy", "source_type", "group_id", "type", "status"} {
			if raw, ok := value[key]; ok && raw != nil {
				if _, ok := raw.(string); !ok {
					return nil, invalid(fmt.Sprintf("第 %d 条账号字段类型无效", i+1))
				}
			}
		}
		for _, key := range []string{"enabled", "image_quota_unknown"} {
			if raw, ok := value[key]; ok && raw != nil {
				if _, ok := raw.(bool); !ok {
					return nil, invalid(fmt.Sprintf("第 %d 条账号状态必须是布尔值", i+1))
				}
			}
		}
		if !validateToken(text(value["access_token"])) || (text(value["refresh_token"]) != "" && !validateToken(text(value["refresh_token"]))) || (text(value["id_token"]) != "" && !validateToken(text(value["id_token"]))) {
			return nil, invalid(fmt.Sprintf("第 %d 条凭据无效或超过 64 KiB", i+1))
		}
		if err := validateReference(text(value["proxy"])); err != nil {
			return nil, err
		}
		if req.TargetGroup != nil {
			value["group_id"] = strings.TrimSpace(*req.TargetGroup)
		}
		if !req.Restore {
			delete(value, "management_id")
			delete(value, "id")
			delete(value, "access_token_fingerprints")
			for _, key := range []string{"refresh_token_invalid_at", "last_token_refresh_error", "last_remote_check_error", "last_refresh_error"} {
				delete(value, key)
			}
		}
		if strings.EqualFold(text(value["type"]), "codex") || text(value["export_type"]) == "codex" {
			value["source_type"] = "codex"
			delete(value, "type")
		}
		if text(value["type"]) == "" {
			value["type"] = text(value["plan_type"])
		}
	}
	return values, nil
}

type record struct {
	row  int
	data map[string]any
}
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func load(ctx context.Context, q queryer) ([]record, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,access_token,data FROM accounts ORDER BY id")
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	items := []record{}
	ids := map[string]bool{}
	for rows.Next() {
		var rec record
		var token, data string
		if rows.Scan(&rec.row, &token, &data) != nil || decodeJSON([]byte(data), &rec.data) != nil || rec.data == nil || text(rec.data["access_token"]) != token {
			return nil, ErrStorage
		}
		id := accountID(rec.data)
		if ids[id] {
			return nil, ErrStorage
		}
		ids[id] = true
		rec.data["management_id"] = id
		rec.data["id"] = id
		items = append(items, rec)
	}
	if rows.Err() != nil {
		return nil, ErrStorage
	}
	return items, nil
}
func (r *Repository) snapshot(parent context.Context) ([]record, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	return load(ctx, r.client)
}
func (r *Repository) mutate(parent context.Context, expected string, fn func(context.Context, *ent.Tx, []record, *Operation) (bool, error)) (Operation, error) {
	result := emptyOperation()
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return result, ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('proxy_configuration'))"); err != nil {
		return result, ErrStorage
	}
	rows, err := tx.QueryContext(ctx, "SELECT version FROM storage_revisions WHERE collection='accounts' FOR UPDATE")
	if err != nil {
		return result, ErrStorage
	}
	var revision int
	found := rows.Next()
	if found {
		err = rows.Scan(&revision)
	}
	rowerr := rows.Err()
	rows.Close()
	if !found || err != nil || rowerr != nil {
		return result, ErrStorage
	}
	current := fmt.Sprintf("accounts:%d", revision)
	if expected != "" && expected != current {
		return result, ErrConflict
	}
	items, err := load(ctx, tx)
	if err != nil {
		return result, err
	}
	changed, err := fn(ctx, tx, items, &result)
	if err != nil {
		return result, err
	}
	if changed {
		if _, err = tx.ExecContext(ctx, "UPDATE storage_revisions SET version=version+1 WHERE collection='accounts'"); err != nil {
			return result, ErrStorage
		}
		revision++
	}
	if tx.Commit() != nil {
		return result, ErrStorage
	}
	result.Revision = fmt.Sprintf("accounts:%d", revision)
	present(&result)
	return result, nil
}
func save(ctx context.Context, tx *ent.Tx, rec record) error {
	encoded, err := json.Marshal(rec.data)
	if err != nil {
		return ErrStorage
	}
	if rec.row == 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO accounts(access_token,data) VALUES($1,$2)", text(rec.data["access_token"]), string(encoded))
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE accounts SET access_token=$1,data=$2 WHERE id=$3", text(rec.data["access_token"]), string(encoded), rec.row)
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}
func emptyOperation() Operation {
	return Operation{UpdatedIDs: []string{}, RemovedIDs: []string{}, Errors: []any{}, Events: []map[string]any{}, Items: []any{}, SummaryItems: []map[string]any{}}
}
func present(result *Operation) {
	result.StatusLabel = "处理完成"
	result.Tone = "success"
	result.Message = "账号操作已完成"
	if len(result.Errors) > 0 {
		result.StatusLabel = "部分失败"
		result.Tone = "warning"
		result.Message = "部分账号处理失败，请查看操作事件"
	}
	result.SummaryItems = []map[string]any{}
	for _, entry := range []struct {
		k, l string
		n    int
	}{{"added", "新增", result.Added}, {"skipped", "跳过", result.Skipped}, {"updated", "更新", result.Updated}, {"removed", "删除", result.Removed}, {"synced", "同步", result.Synced}, {"refreshed", "续期", result.Refreshed}, {"failed", "失败", len(result.Errors)}} {
		result.SummaryItems = append(result.SummaryItems, map[string]any{"key": entry.k, "label": entry.l, "value": entry.n, "tone": result.Tone})
	}
}
func event(result *Operation, id, action, status, message string) {
	tone := "success"
	if status == "failed" {
		tone = "danger"
	}
	if status == "skipped" {
		tone = "warning"
	}
	result.Events = append(result.Events, map[string]any{"sequence": len(result.Events) + 1, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "account_id": id, "account_label": id, "action": action, "status": status, "tone": tone, "message": message})
}
func operationError(result *Operation, id, code, message, action string) {
	result.Errors = append(result.Errors, map[string]any{"id": id, "code": code, "message": message})
	event(result, id, action, "failed", message)
}
func normalize(value map[string]any) map[string]any {
	out := clone(value)
	id := accountID(out)
	out["management_id"] = id
	out["id"] = id
	out["access_token"] = text(out["access_token"])
	out["source_type"] = normalizeSource(text(out["source_type"]))
	if text(out["type"]) == "" {
		claims := claims(text(out["access_token"]))
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			out["type"] = text(auth["chatgpt_plan_type"])
		}
		if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok && text(out["email"]) == "" {
			out["email"] = text(profile["email"])
		}
	}
	status := text(out["status"])
	aliases := map[string]string{"active": "正常", "normal": "正常", "limited": "限流", "rate_limited": "限流", "invalid": "异常", "disabled": "禁用", "abnormal": "异常"}
	if mapped := aliases[strings.ToLower(status)]; mapped != "" {
		status = mapped
	}
	if status == "" {
		status = "正常"
	}
	if !flag(out["enabled"], true) {
		status = "禁用"
	}
	out["status"] = status
	delete(out, "enabled")
	if out["image_quota_unknown"] == nil {
		out["image_quota_unknown"] = true
		if limits, ok := out["limits_progress"].([]any); ok {
			for _, raw := range limits {
				if m, ok := raw.(map[string]any); ok {
					feature := strings.ToLower(first(m["feature_name"], m["feature"], m["name"], m["type"]))
					if feature == "image_gen" || feature == "image_generation" || feature == "image" || feature == "images" {
						out["image_quota_unknown"] = false
						if out["quota"] == nil {
							out["quota"] = max(0, number(m["remaining"]))
						}
						if out["restore_at"] == nil {
							out["restore_at"] = first(m["reset_after"], m["restore_at"], m["reset_at"])
						}
						break
					}
				}
			}
		}
	}
	if status == "限流" {
		out["quota"] = 0
		out["image_quota_unknown"] = false
	}
	if out["quota"] == nil {
		out["quota"] = 0
	}
	if text(out["created_at"]) == "" && number(out["created_at"]) == 0 {
		out["created_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	fps := []any{}
	if list, ok := out["access_token_fingerprints"].([]any); ok {
		for _, raw := range list {
			if fp := text(raw); len(fp) == 64 && fp != digest(text(out["access_token"])) {
				fps = append(fps, fp)
			}
		}
	}
	if len(fps) > 7 {
		fps = append(fps[:1], fps[len(fps)-6:]...)
	}
	fps = append(fps, digest(text(out["access_token"])))
	out["access_token_fingerprints"] = fps
	return out
}
func (r *Repository) Import(parent context.Context, req ImportRequest) (Operation, error) {
	payloads, err := req.payloads()
	if err != nil {
		return emptyOperation(), err
	}
	release, err := r.processingSlot(parent)
	if err != nil {
		return emptyOperation(), err
	}
	defer release()
	result, err := r.mutate(parent, req.Revision, func(ctx context.Context, tx *ent.Tx, items []record, result *Operation) (bool, error) {
		groups, err := loadGroups(ctx, tx)
		if err != nil {
			return false, err
		}
		for _, item := range payloads {
			if group := text(item["group_id"]); group != "" && !hasGroup(groups, group) {
				return false, ErrNotFound
			}
			if err := validateProxyGroup(ctx, tx, text(item["proxy"])); err != nil {
				return false, err
			}
		}
		byToken := map[string]record{}
		byFingerprint := map[string]string{}
		idOwners := map[string]string{}
		for _, rec := range items {
			token := text(rec.data["access_token"])
			byToken[token] = rec
			idOwners[accountID(rec.data)] = token
			byFingerprint[digest(token)] = token
			if list, ok := rec.data["access_token_fingerprints"].([]any); ok {
				for _, fp := range list {
					byFingerprint[text(fp)] = token
				}
			}
		}
		ordered := []string{}
		merged := map[string]map[string]any{}
		for _, item := range payloads {
			token := text(item["access_token"])
			if old, exists := merged[token]; exists {
				for k, v := range item {
					old[k] = v
				}
				result.Skipped++
			} else {
				ordered = append(ordered, token)
				merged[token] = clone(item)
			}
		}
		changed := false
		for _, token := range ordered {
			incoming := merged[token]
			rec, exists := byToken[token]
			if owner := byFingerprint[digest(token)]; owner != "" && owner != token {
				result.Skipped++
				event(result, accountID(byToken[owner].data), "import_account", "skipped", "旧令牌已轮换，保留现有凭据")
				continue
			}
			before := clone(rec.data)
			if !exists {
				rec.data = map[string]any{}
				result.Added++
			} else {
				result.Skipped++
			}
			for k, v := range incoming {
				rec.data[k] = v
			}
			if exists {
				rec.data["management_id"] = accountID(before)
				if text(before["refresh_token"]) != text(rec.data["refresh_token"]) {
					rec.data["refresh_token_invalid_at"] = nil
				}
			}
			rec.data = normalize(rec.data)
			id := accountID(rec.data)
			if owner := idOwners[id]; owner != "" && owner != token {
				return false, ErrConflict
			}
			idOwners[id] = token
			if !exists || !reflect.DeepEqual(before, rec.data) {
				if err := save(ctx, tx, rec); err != nil {
					return false, err
				}
				changed = true
			}
			result.UpdatedIDs = append(result.UpdatedIDs, id)
			if req.ReturnItems == nil || *req.ReturnItems {
				result.Items = append(result.Items, project(rec.data, nil, 0))
			}
			status := "success"
			if exists {
				status = "skipped"
			}
			event(result, id, "import_account", status, "账号已保存")
		}
		return changed, nil
	})
	return result, err
}
