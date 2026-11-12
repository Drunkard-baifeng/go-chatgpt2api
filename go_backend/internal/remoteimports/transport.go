// Package remoteimports 拥有 CPA/sub2api 远程配置、导入任务和手动 OAuth 会话。
package remoteimports

import (
	"bytes"
	"chatgpt2api/go_backend/internal/managedhttp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

var ErrRemote = errors.New("远程服务请求失败，请检查配置、权限和网络")
var ErrStorage = errors.New("远程导入仓储暂不可用")
var ErrNotFound = errors.New("远程导入来源不存在")
var ErrConflict = errors.New("已有远程账号导入任务正在执行")
var ErrInput = errors.New("远程导入参数无效或超过限制")

const maxRemoteBytes = 8 << 20
const maxSources = 100
const maxRemoteItems = 10000

type managementClient struct{ client *managedhttp.Client }

func managementURL(raw string) (*url.URL, error) {
	u, e := managedhttp.ParseURL(raw)
	if e != nil {
		return nil, ErrInput
	}
	return u, nil
}
func newManagementClient(raw string) (*managementClient, error) {
	client, e := managedhttp.New(raw)
	if e != nil {
		return nil, ErrInput
	}
	return &managementClient{client}, nil
}
func (m *managementClient) Close() { m.client.Close() }
func safeNetwork(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return ErrRemote
}
func (m *managementClient) json(ctx context.Context, method, path string, body any, headers http.Header) (any, error) {
	var data []byte
	var e error
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil || len(data) > 1<<20 {
			return nil, ErrInput
		}
	}
	headers = headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Accept", "application/json")
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}
	raw, status, e := m.client.Request(ctx, method, path, bytes.NewReader(data), headers, maxRemoteBytes)
	if e != nil {
		return nil, safeNetwork(ctx, e)
	}
	if status < 200 || status >= 300 {
		return nil, ErrRemote
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if decoder.Decode(&out) != nil {
		return nil, ErrRemote
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return nil, ErrRemote
	}
	return out, nil
}
