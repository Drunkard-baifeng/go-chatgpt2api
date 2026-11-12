package remoteimports

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func first(values ...any) string {
	for _, v := range values {
		if s := text(v); s != "" {
			return s
		}
	}
	return ""
}
func bounded(value any, size int) string {
	s := text(value)
	if len(s) > size || strings.ContainsAny(s, "\r\n\x00") {
		return ""
	}
	return s
}
func section(account map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v := object(account[k]); len(v) > 0 {
			return v
		}
	}
	return nil
}
func unwrap(v any) any {
	m := object(v)
	if data, ok := m["data"]; ok {
		_, hasCode := m["code"]
		onlyEnvelope := true
		for key := range m {
			switch key {
			case "code", "message", "msg", "data", "success", "status":
			default:
				onlyEnvelope = false
			}
		}
		if hasCode || onlyEnvelope {
			return data
		}
	}
	return v
}
func paged(v any) ([]any, int, error) {
	v = unwrap(v)
	if list, ok := v.([]any); ok {
		return list, len(list), nil
	}
	m := object(v)
	if list, ok := m["data"].([]any); ok {
		return list, max(integer(m["total"]), len(list)), nil
	}
	for _, candidate := range []map[string]any{m, object(m["data"])} {
		for _, key := range []string{"items", "list", "accounts", "records", "rows", "groups"} {
			if list, ok := candidate[key].([]any); ok {
				return list, max(integer(candidate["total"]), max(integer(m["total"]), len(list))), nil
			}
		}
	}
	return nil, 0, ErrRemote
}
func (m *managementClient) auth(ctx context.Context, provider string, source map[string]any) (http.Header, error) {
	if provider == "cpa" {
		return http.Header{"Authorization": {"Bearer " + text(source["secret_key"])}}, nil
	}
	if key := text(source["api_key"]); key != "" {
		return http.Header{"X-Api-Key": {key}}, nil
	}
	payload, e := m.json(ctx, "POST", "/api/v1/auth/login", map[string]string{"email": text(source["email"]), "password": text(source["password"])}, nil)
	if e != nil {
		return nil, e
	}
	token := bounded(object(unwrap(payload))["access_token"], 65536)
	if token == "" {
		return nil, ErrRemote
	}
	return http.Header{"Authorization": {"Bearer " + token}}, nil
}
func (s *Service) ListRemote(parent context.Context, provider, id, kind string, groupOverride *string) ([]map[string]any, error) {
	if (provider == "cpa" && kind != "files") || (provider == "sub2api" && kind != "accounts" && kind != "groups") || (groupOverride != nil && (len(*groupOverride) > 128 || strings.ContainsAny(*groupOverride, "\r\n\x00"))) {
		return nil, ErrInput
	}
	source, e := s.source(parent, provider, id)
	if e != nil {
		return nil, e
	}
	m, e := newManagementClient(text(source["base_url"]))
	if e != nil {
		return nil, e
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if s.ctx.Err() != nil {
		return nil, context.Canceled
	}
	headers, e := m.auth(ctx, provider, source)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	if provider == "cpa" {
		payload, e := m.json(ctx, "GET", "/v0/management/auth-files", nil, headers)
		if e != nil {
			return nil, e
		}
		files, ok := object(payload)["files"].([]any)
		if !ok || len(files) > maxRemoteItems {
			return nil, ErrRemote
		}
		for _, raw := range files {
			item := object(raw)
			name := bounded(item["name"], 512)
			if name != "" {
				out = append(out, map[string]any{"name": name, "email": bounded(first(item["email"], item["account"]), 512)})
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	group := text(source["group_id"])
	if groupOverride != nil {
		group = strings.TrimSpace(*groupOverride)
	}
	for page := 1; page <= 50; page++ {
		query := url.Values{"page": {text(page)}, "page_size": {"200"}}
		if kind == "accounts" {
			query.Set("platform", "openai")
			query.Set("type", "oauth")
			query.Set("account_type", "oauth")
			if group != "" {
				query.Set("group", group)
				query.Set("group_id", group)
			}
		}
		payload, e := m.json(ctx, "GET", "/api/v1/admin/"+kind+"?"+query.Encode(), nil, headers)
		if e != nil {
			return nil, e
		}
		items, total, e := paged(payload)
		if e != nil || len(items) > 200 || total > maxRemoteItems {
			return nil, ErrRemote
		}
		newItems := 0
		for _, raw := range items {
			item := object(raw)
			itemID := bounded(item["id"], 128)
			if itemID == "" || seen[itemID] {
				continue
			}
			seen[itemID] = true
			newItems++
			if kind == "groups" {
				out = append(out, map[string]any{"id": itemID, "name": bounded(item["name"], 256), "description": bounded(item["description"], 2048), "platform": bounded(item["platform"], 64), "status": bounded(item["status"], 64), "account_count": max(0, integer(item["account_count"])), "active_account_count": max(0, integer(item["active_account_count"]))})
				continue
			}
			credentials := section(item, "credentials", "credential", "tokens", "auth")
			extra := section(item, "extra", "metadata", "meta")
			gid, gname := remoteGroup(item, extra)
			if gid == "" {
				gid = group
			}
			out = append(out, map[string]any{"id": itemID, "name": bounded(item["name"], 256), "email": bounded(first(credentials["email"], extra["email"], item["email"], item["name"]), 512), "plan_type": bounded(first(credentials["plan_type"], credentials["planType"], extra["plan_type"], item["plan_type"]), 128), "status": bounded(item["status"], 64), "expires_at": bounded(credentials["expires_at"], 128), "has_access_token": first(credentials["access_token"], credentials["accessToken"], credentials["token"], item["access_token"]) != "", "remote_group_id": gid, "remote_group_name": gname})
		}
		if len(out) > maxRemoteItems {
			return nil, ErrRemote
		}
		if len(items) == 0 || page*200 >= total || len(items) < 200 {
			return out, nil
		}
		if newItems == 0 {
			return nil, ErrRemote
		}
	}
	return nil, ErrRemote
}
func remoteGroup(item, extra map[string]any) (string, string) {
	for _, m := range []map[string]any{item, extra} {
		for _, key := range []string{"group", "group_info", "groupInfo", "account_group", "accountGroup"} {
			if v := object(m[key]); v != nil {
				return bounded(first(v["id"], v["group_id"], v["groupId"]), 128), bounded(first(v["name"], v["group_name"]), 256)
			}
		}
		if list, ok := m["groups"].([]any); ok {
			for _, raw := range list {
				v := object(raw)
				if v != nil {
					return bounded(v["id"], 128), bounded(v["name"], 256)
				}
			}
		}
	}
	return bounded(first(item["group_id"], item["groupId"], extra["group_id"]), 128), bounded(first(item["group_name"], item["groupName"], extra["group_name"]), 256)
}
func importPayload(raw any) (map[string]any, error) {
	item := object(raw)
	nested := section(item, "credentials", "credential", "tokens", "auth")
	out := map[string]any{"source_type": "codex"}
	for key, aliases := range map[string][]string{"access_token": {"access_token", "accessToken", "token", "cookie"}, "refresh_token": {"refresh_token", "refreshToken"}, "id_token": {"id_token", "idToken"}} {
		for _, source := range []map[string]any{nested, item} {
			for _, alias := range aliases {
				if v := text(source[alias]); v != "" {
					if len(v) > 65536 || strings.ContainsAny(v, "\r\n\x00") {
						return nil, ErrRemote
					}
					out[key] = v
					break
				}
			}
			if out[key] != nil {
				break
			}
		}
	}
	if out["access_token"] == nil {
		return nil, ErrRemote
	}
	extra := section(item, "extra", "metadata", "meta")
	if email := bounded(first(nested["email"], extra["email"], item["email"]), 512); email != "" {
		out["email"] = email
	}
	if plan := bounded(first(nested["plan_type"], nested["planType"], extra["plan_type"], item["plan_type"]), 128); plan != "" {
		out["type"] = plan
	}
	return out, nil
}
func fetchCPA(ctx context.Context, m *managementClient, headers http.Header, name string) (map[string]any, error) {
	payload, e := m.json(ctx, "GET", "/v0/management/auth-files/download?"+url.Values{"name": {name}}.Encode(), nil, headers)
	if e != nil {
		return nil, e
	}
	return importPayload(payload)
}
func fetchSubBatch(ctx context.Context, m *managementClient, headers http.Header, ids []string) (map[string]map[string]any, error) {
	payload, e := m.json(ctx, "GET", "/api/v1/admin/accounts/data?"+url.Values{"ids": {strings.Join(ids, ",")}, "include_proxies": {"false"}, "timezone": {"Asia/Shanghai"}}.Encode(), nil, headers)
	if e != nil {
		return nil, e
	}
	body := object(unwrap(payload))
	list, ok := body["accounts"].([]any)
	if !ok {
		list, ok = object(body["data"])["accounts"].([]any)
	}
	if !ok || len(list) > len(ids) {
		return nil, ErrRemote
	}
	requested := map[string]bool{}
	for _, id := range ids {
		requested[id] = true
	}
	out := map[string]map[string]any{}
	for _, raw := range list {
		item := object(raw)
		id := first(item["id"], item["account_id"], section(item, "credentials")["id"])
		if id == "" && len(ids) == 1 {
			id = ids[0]
		}
		if !requested[id] {
			continue
		}
		value, e := importPayload(item)
		if e == nil {
			out[id] = value
		}
	}
	return out, nil
}
func fetchSubDetail(ctx context.Context, m *managementClient, headers http.Header, id string) (map[string]any, error) {
	payload, e := m.json(ctx, "GET", "/api/v1/admin/accounts/"+url.PathEscape(id), nil, headers)
	if e != nil {
		return nil, e
	}
	item := object(unwrap(payload))
	for i := 0; i < 3; i++ {
		next := section(item, "account", "item", "record", "data")
		if next == nil {
			break
		}
		item = next
	}
	if got := first(item["id"], item["account_id"]); got != "" && got != id {
		return nil, ErrRemote
	}
	return importPayload(item)
}
