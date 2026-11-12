package app

import (
	"archive/zip"
	"bytes"
	"chatgpt2api/go_backend/internal/accounts"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func accountError(c *gin.Context, err error) {
	var input *accounts.InputError
	switch {
	case errors.As(err, &input):
		writeError(c, 400, "invalid_request_error", input.Message)
	case errors.Is(err, accounts.ErrNotFound):
		writeError(c, 404, "not_found_error", accounts.ErrNotFound.Error())
	case errors.Is(err, accounts.ErrConflict):
		writeError(c, 409, "conflict_error", accounts.ErrConflict.Error())
	case errors.Is(err, accounts.ErrBusy):
		writeError(c, 429, "rate_limit_error", accounts.ErrBusy.Error())
	case errors.Is(err, context.Canceled):
		writeError(c, 499, "server_error", "操作已取消")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "server_error", "操作超时")
	default:
		writeError(c, 503, "server_error", accounts.ErrStorage.Error())
	}
}
func accountBody(c *gin.Context, value any) bool {
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, accounts.MaxControlBytes)
	if err := accounts.Decode(c.Request.Body, value); err != nil {
		accountError(c, err)
		return false
	}
	return true
}
func registerAccountRoutes(router *gin.Engine, options Options) {
	register := func(method, path string, handler gin.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			if textSubject(c, options.Auth, true) == nil {
				return
			}
			c.Header("Cache-Control", "no-store")
			if options.Accounts == nil {
				accountError(c, accounts.ErrStorage)
				return
			}
			handler(c)
		})
	}
	register("GET", "/api/accounts", func(c *gin.Context) {
		page, size := 1, 500
		var err error
		if raw := c.Query("page"); raw != "" {
			page, err = strconv.Atoi(raw)
			if err != nil || page < 1 {
				writeError(c, 400, "invalid_request_error", "页码无效")
				return
			}
		}
		if raw := c.Query("page_size"); raw != "" {
			size, err = strconv.Atoi(raw)
			if err != nil || size < 1 || size > 500 {
				writeError(c, 400, "invalid_request_error", "每页数量必须为 1 到 500")
				return
			}
		}
		result, err := options.Accounts.List(c.Request.Context(), accounts.Filter{Page: page, PageSize: size, Keyword: c.Query("keyword"), Status: c.Query("status"), GroupID: c.Query("group_id")})
		if err != nil {
			accountError(c, err)
			return
		}
		c.JSON(200, result)
	})
	register("POST", "/api/accounts", func(c *gin.Context) {
		var request accounts.ImportRequest
		if !accountBody(c, &request) {
			return
		}
		result, err := options.Accounts.Import(c.Request.Context(), request)
		if err != nil {
			accountError(c, err)
			return
		}
		if request.ShouldSync() {
			_ = options.Accounts.SyncImported(c.Request.Context(), &result)
		}
		if request.ReturnItems != nil && !*request.ReturnItems {
			result.Items = []any{}
		}
		c.JSON(200, result)
	})
	register("GET", "/api/accounts/:account_id", func(c *gin.Context) {
		result, err := options.Accounts.Detail(c.Request.Context(), c.Param("account_id"))
		if err != nil {
			accountError(c, err)
			return
		}
		c.JSON(200, gin.H{"item": result})
	})
	for _, field := range []string{"access_token", "refresh_token"} {
		path := strings.ReplaceAll(field, "_", "-")
		register("GET", "/api/accounts/:account_id/"+path, func(c *gin.Context) {
			token, err := options.Accounts.Token(c.Request.Context(), c.Param("account_id"), field)
			if err != nil {
				accountError(c, err)
				return
			}
			c.JSON(200, gin.H{field: token})
		})
	}
	register("POST", "/api/accounts/selection-preview", func(c *gin.Context) {
		var body struct {
			Selection accounts.Selection `json:"selection"`
		}
		if !accountBody(c, &body) {
			return
		}
		result, err := options.Accounts.Preview(c.Request.Context(), body.Selection)
		if err != nil {
			accountError(c, err)
			return
		}
		c.JSON(200, result)
	})
	register("POST", "/api/accounts/update", func(c *gin.Context) {
		var body map[string]any
		if !accountBody(c, &body) {
			return
		}
		result, err := options.Accounts.Update(c.Request.Context(), body)
		if err != nil {
			accountError(c, err)
			return
		}
		c.JSON(200, result)
	})
	for _, route := range []struct{ method, path, action string }{{"DELETE", "/api/accounts", "delete_account"}, {"POST", "/api/accounts/batch-update", "update_account"}, {"POST", "/api/accounts/group", "bind_account_group"}} {
		register(route.method, route.path, func(c *gin.Context) {
			var cmd accounts.Command
			if !accountBody(c, &cmd) {
				return
			}
			if cmd.GroupID == "__ungrouped__" {
				cmd.GroupID = ""
			}
			result, err := options.Accounts.Batch(c.Request.Context(), cmd, route.action)
			if err != nil {
				accountError(c, err)
				return
			}
			if route.action == "bind_account_group" {
				groups, err := options.Accounts.Groups(c.Request.Context())
				if err != nil {
					accountError(c, err)
					return
				}
				payload := map[string]any{}
				data, _ := json.Marshal(result)
				_ = json.Unmarshal(data, &payload)
				for k, v := range groups {
					payload[k] = v
				}
				payload["group_id"] = cmd.GroupID
				c.JSON(200, payload)
				return
			}
			id, err := options.Accounts.Completed(result)
			if err != nil {
				accountError(c, err)
				return
			}
			c.JSON(200, gin.H{"progress_id": id, "target_ids": append(result.UpdatedIDs, result.RemovedIDs...), "errors": result.Errors})
		})
	}
	for _, route := range []struct{ path, action string }{{"/api/accounts/sync", "sync_account"}, {"/api/accounts/refresh", "sync_account"}, {"/api/accounts/refresh-access-token", "refresh_access_token"}} {
		register("POST", route.path, func(c *gin.Context) {
			var cmd accounts.Command
			if !accountBody(c, &cmd) {
				return
			}
			result, err := options.Accounts.Start(c.Request.Context(), cmd, route.action)
			if err != nil {
				accountError(c, err)
				return
			}
			c.JSON(200, result)
		})
	}
	for _, path := range []string{"/api/accounts/operations/:progress_id", "/api/accounts/refresh/progress/:progress_id"} {
		register("GET", path, func(c *gin.Context) {
			result, err := options.Accounts.Progress(c.Param("progress_id"))
			if err != nil {
				accountError(c, err)
				return
			}
			c.JSON(200, result)
		})
	}
	register("GET", "/api/account-groups", func(c *gin.Context) {
		result, err := options.Accounts.Groups(c.Request.Context())
		if err != nil {
			accountError(c, err)
			return
		}
		enrichProxyGroups(c, options, result)
		c.JSON(200, result)
	})
	register("POST", "/api/account-groups", func(c *gin.Context) {
		var body map[string]any
		if !accountBody(c, &body) {
			return
		}
		result, err := options.Accounts.SaveGroup(c.Request.Context(), body, "")
		if err != nil {
			accountError(c, err)
			return
		}
		enrichProxyGroups(c, options, result)
		c.JSON(200, result)
	})
	register("DELETE", "/api/account-groups/:group_id", func(c *gin.Context) {
		result, err := options.Accounts.SaveGroup(c.Request.Context(), nil, c.Param("group_id"))
		if err != nil {
			accountError(c, err)
			return
		}
		enrichProxyGroups(c, options, result)
		c.JSON(200, result)
	})
	register("POST", "/api/accounts/export", func(c *gin.Context) {
		var cmd accounts.Command
		if !accountBody(c, &cmd) {
			return
		}
		if cmd.Format == "" {
			cmd.Format = "json"
		}
		if cmd.Format != "json" && cmd.Format != "txt" && cmd.Format != "zip" {
			writeError(c, 400, "invalid_request_error", "导出格式无效")
			return
		}
		items, err := options.Accounts.Export(c.Request.Context(), cmd)
		if err != nil {
			accountError(c, err)
			return
		}
		count := strconv.Itoa(len(items))
		c.Header("X-Export-Requested", count)
		c.Header("X-Exported", count)
		c.Header("X-Skipped", "0")
		name := "accounts-" + time.Now().UTC().Format("20060102-150405") + "." + cmd.Format
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		switch cmd.Format {
		case "txt":
			var out strings.Builder
			for _, item := range items {
				token, _ := item["access_token"].(string)
				out.WriteString(token + "\n")
			}
			c.Data(200, "text/plain; charset=utf-8", []byte(out.String()))
		case "zip":
			var out bytes.Buffer
			archive := zip.NewWriter(&out)
			for i, item := range items {
				raw, _ := item["email"].(string)
				base := safeArchiveName.ReplaceAllString(raw, "_")
				if base == "" {
					base = "account"
				}
				if len(base) > 80 {
					base = base[:80]
				}
				entry, e := archive.Create(fmt.Sprintf("%03d-%s.json", i+1, base))
				if e != nil {
					accountError(c, accounts.ErrStorage)
					return
				}
				if json.NewEncoder(entry).Encode(item) != nil {
					accountError(c, accounts.ErrStorage)
					return
				}
			}
			if archive.Close() != nil {
				accountError(c, accounts.ErrStorage)
				return
			}
			c.Data(200, "application/zip", out.Bytes())
		default:
			var out any = items
			if len(items) == 1 {
				out = items[0]
			}
			c.JSON(200, out)
		}
	})
	register("POST", "/api/accounts/import-cleanup", func(c *gin.Context) {
		var cmd accounts.Command
		if !accountBody(c, &cmd) {
			return
		}
		if len(cmd.AccountIDs)+len(cmd.Tokens)+len(cmd.AccessTokens) == 0 && (cmd.Selection == nil || cmd.Selection.Mode != "explicit" || len(cmd.Selection.AccountIDs) == 0) {
			writeError(c, 400, "invalid_request_error", "导入清理必须明确指定本次导入账号")
			return
		}
		items, err := options.Accounts.Export(c.Request.Context(), cmd)
		if err != nil {
			accountError(c, err)
			return
		}
		ids := []string{}
		for _, item := range items {
			if item["status"] == "异常" {
				id, _ := item["management_id"].(string)
				ids = append(ids, id)
			}
		}
		result := gin.H{"checked": len(items), "abnormal": len(ids), "removed": 0, "updated_ids": []string{}, "removed_ids": []string{}, "errors": []any{}, "events": []any{}}
		if cmd.Remove && len(ids) > 0 {
			operation, err := options.Accounts.Batch(c.Request.Context(), accounts.Command{AccountIDs: ids}, "delete_account")
			if err != nil {
				accountError(c, err)
				return
			}
			result["removed"] = operation.Removed
			result["removed_ids"] = operation.RemovedIDs
			result["errors"] = operation.Errors
			result["events"] = operation.Events
		}
		c.JSON(200, result)
	})
}

var safeArchiveName = regexp.MustCompile(`[^a-zA-Z0-9._@-]`)

func enrichProxyGroups(c *gin.Context, options Options, result map[string]any) {
	if options.Proxy == nil {
		return
	}
	view, err := options.Proxy.View(c.Request.Context())
	if err == nil {
		if groups, ok := view["groups"]; ok {
			result["proxy_groups"] = groups
		}
	}
}
