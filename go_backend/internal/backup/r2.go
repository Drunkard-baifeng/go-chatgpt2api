// Package backup 拥有 R2 备份执行、归档和调度生命周期。
package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/assettransfer"
)

var (
	ErrInput       = errors.New("R2 备份配置或参数无效")
	ErrRemote      = errors.New("R2 请求失败，请检查服务、权限或网络")
	ErrStorage     = errors.New("备份状态存储暂不可用")
	ErrBusy        = errors.New("当前已有备份任务正在执行")
	ErrClosed      = errors.New("备份服务已关闭")
	ErrLimit       = errors.New("备份超过文件数量或 512 MiB 大小上限")
	ErrSnapshot    = errors.New("备份包含项尚未提供有效快照，未上传不完整备份")
	ErrDumpMissing = errors.New("当前环境缺少 pg_dump，无法备份 PostgreSQL")
	ErrDump        = errors.New("PostgreSQL 备份失败，请检查 pg_dump 版本、数据库权限和连接")
)

const maxBackupBytes int64 = 512 << 20

var accountPattern = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var backupNamePattern = regexp.MustCompile(`^backup-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{4,32}\.tar\.gz(\.enc)?$`)

type backupConfig struct {
	Enabled                                                 bool
	AccountID, AccessKeyID, SecretAccessKey, Bucket, Prefix string
	Encrypt                                                 bool
	Passphrase                                              string
	IntervalMinutes                                         int
	RotationKeep                                            int
	Include                                                 map[string]bool
}
type Item struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	SizeBytes    int64  `json:"size_bytes"`
	UpdatedAt    string `json:"updated_at"`
	LastModified string `json:"last_modified"`
	Encrypted    bool   `json:"encrypted"`
}
type r2Client struct {
	config backupConfig
	do     func(*http.Request) (*http.Response, error)
}

func newR2(cfg backupConfig) (*r2Client, error) {
	if cfg.Prefix == "" {
		cfg.Prefix = "backups"
	}
	cfg.Prefix = strings.TrimSuffix(cfg.Prefix, "/")
	if !accountPattern.MatchString(cfg.AccountID) || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || len(cfg.AccessKeyID) > 256 || strings.ContainsAny(cfg.AccessKeyID, "\r\n/,") || len(cfg.SecretAccessKey) > 4096 || !bucketPattern.MatchString(cfg.Bucket) || !validPrefix(cfg.Prefix) {
		return nil, ErrInput
	}
	return &r2Client{config: cfg, do: pinnedRequest}, nil
}
func validPrefix(prefix string) bool {
	if len(prefix) == 0 || len(prefix) > 512 || strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\\\r\n\x00") {
		return false
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func (c *r2Client) Close() {}
func (c *r2Client) owns(key string) bool {
	return strings.HasPrefix(key, c.config.Prefix+"/") && path.Dir(key) == c.config.Prefix && backupNamePattern.MatchString(path.Base(key))
}
func hmacBytes(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func shaHex(data []byte) string            { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
func encodeQuery(values url.Values) string { return strings.ReplaceAll(values.Encode(), "+", "%20") }
func (c *r2Client) sign(req *http.Request, payloadHash string, at time.Time) {
	amzDate, dateStamp := at.UTC().Format("20060102T150405Z"), at.UTC().Format("20060102")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	headers := map[string]string{"host": req.Host}
	for key, values := range req.Header {
		headers[strings.ToLower(key)] = strings.Join(strings.Fields(strings.Join(values, ",")), " ")
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, key := range keys {
		canonicalHeaders.WriteString(key + ":" + headers[key] + "\n")
	}
	signedHeaders := strings.Join(keys, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonicalHeaders.String(), signedHeaders, payloadHash}, "\n")
	scope := dateStamp + "/auto/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + shaHex([]byte(canonical))
	key := hmacBytes([]byte("AWS4"+c.config.SecretAccessKey), dateStamp)
	key = hmacBytes(key, "auto")
	key = hmacBytes(key, "s3")
	key = hmacBytes(key, "aws4_request")
	signature := hex.EncodeToString(hmacBytes(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.config.AccessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}
func (c *r2Client) request(parent context.Context, method, key string, query url.Values, body io.ReadSeeker, size int64, headers map[string]string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	if size < 0 || size > maxBackupBytes {
		return nil, 0, ErrLimit
	}
	payloadHash := shaHex(nil)
	if body != nil {
		h := sha256.New()
		n, err := io.Copy(h, &contextReader{ctx: ctx, reader: io.LimitReader(body, maxBackupBytes+1)})
		if err != nil || n != size {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			return nil, 0, ErrLimit
		}
		payloadHash = hex.EncodeToString(h.Sum(nil))
		if _, err = body.Seek(0, io.SeekStart); err != nil {
			return nil, 0, ErrRemote
		}
	}
	objectPath := "/" + c.config.Bucket
	if key != "" {
		objectPath += "/" + key
	}
	u := &url.URL{Scheme: "https", Host: c.config.AccountID + ".r2.cloudflarestorage.com", Path: objectPath, RawQuery: encodeQuery(query)}
	segments := strings.Split(objectPath, "/")
	for index, segment := range segments {
		segments[index] = strings.ReplaceAll(url.QueryEscape(segment), "+", "%20")
	}
	u.RawPath = strings.Join(segments, "/")
	var reader io.Reader
	if body != nil {
		reader = body
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, 0, ErrInput
	}
	req.Host = u.Host
	req.ContentLength = size
	// 禁止 net/http 因可重放请求体而重试已受理的上传。
	req.GetBody = nil
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	c.sign(req, payloadHash, time.Now())
	response, err := c.do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, ErrRemote
	}
	defer response.Body.Close()
	if response.ContentLength > 4<<20 {
		return nil, response.StatusCode, ErrLimit
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		if ctx.Err() != nil {
			return nil, response.StatusCode, ctx.Err()
		}
		return nil, response.StatusCode, ErrRemote
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, ErrRemote
	}
	return data, response.StatusCode, nil
}
func pinnedRequest(req *http.Request) (*http.Response, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(req.Context(), "ip", req.URL.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, ErrRemote
	}
	for _, ip := range addresses {
		if !assettransfer.PublicAddress(ip) {
			return nil, ErrRemote
		}
	}
	if req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || !strings.HasSuffix(req.URL.Hostname(), ".r2.cloudflarestorage.com") {
		return nil, ErrRemote
	}
	originalName := req.URL.Hostname()
	clone := req.Clone(req.Context())
	target := *req.URL
	target.Host = net.JoinHostPort(addresses[0].Unmap().String(), "443")
	clone.URL = &target
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: originalName}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true, DisableCompression: true}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(clone)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &closingBody{ReadCloser: response.Body, close: transport.CloseIdleConnections}
	return response, nil
}

type closingBody struct {
	io.ReadCloser
	close func()
}

func (b *closingBody) Close() error { err := b.ReadCloser.Close(); b.close(); return err }

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
func (c *r2Client) Test(ctx context.Context) (int, error) {
	_, status, err := c.request(ctx, "GET", "", url.Values{"list-type": {"2"}, "max-keys": {"1"}}, nil, 0, nil)
	return status, err
}
func (c *r2Client) Upload(ctx context.Context, key string, payload io.ReadSeeker, size int64, encrypted bool, trigger string) error {
	if !c.owns(key) {
		return ErrInput
	}
	_, _, err := c.request(ctx, "PUT", key, nil, payload, size, map[string]string{"Content-Type": "application/octet-stream", "X-Amz-Meta-Created-At": time.Now().UTC().Format(time.RFC3339), "X-Amz-Meta-Encrypted": strconv.FormatBool(encrypted), "X-Amz-Meta-Trigger": trigger})
	return err
}
func (c *r2Client) Delete(ctx context.Context, key string) error {
	if !c.owns(key) {
		return ErrInput
	}
	_, status, err := c.request(ctx, "DELETE", key, nil, nil, 0, nil)
	if status == 404 {
		return nil
	}
	return err
}
func (c *r2Client) List(ctx context.Context) ([]Item, error) {
	items := []Item{}
	continuation := ""
	tokens := map[string]bool{}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		query := url.Values{"list-type": {"2"}, "prefix": {c.config.Prefix + "/"}, "max-keys": {"1000"}}
		if continuation != "" {
			query.Set("continuation-token", continuation)
		}
		data, _, err := c.request(ctx, "GET", "", query, nil, 0, nil)
		if err != nil {
			return nil, err
		}
		var result struct {
			XMLName   xml.Name `xml:"ListBucketResult"`
			Truncated bool     `xml:"IsTruncated"`
			Next      string   `xml:"NextContinuationToken"`
			Contents  []struct {
				Key      string `xml:"Key"`
				Size     int64  `xml:"Size"`
				Modified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		decoder := xml.NewDecoder(bytes.NewReader(data))
		if decoder.Decode(&result) != nil || len(result.Contents) > 1000 {
			return nil, ErrRemote
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, ErrRemote
		}
		for _, object := range result.Contents {
			if object.Size < 0 {
				return nil, ErrRemote
			}
			if !c.owns(object.Key) || seen[object.Key] {
				continue
			}
			seen[object.Key] = true
			items = append(items, Item{Key: object.Key, Name: path.Base(object.Key), Size: object.Size, SizeBytes: object.Size, UpdatedAt: object.Modified, LastModified: object.Modified, Encrypted: strings.HasSuffix(object.Key, ".enc")})
		}
		if !result.Truncated {
			sort.Slice(items, func(i, j int) bool {
				if items[i].UpdatedAt != items[j].UpdatedAt {
					return items[i].UpdatedAt > items[j].UpdatedAt
				}
				return items[i].Key > items[j].Key
			})
			return items, nil
		}
		continuation = result.Next
		if continuation == "" || len(continuation) > 8192 || tokens[continuation] {
			return nil, ErrRemote
		}
		tokens[continuation] = true
	}
	return nil, ErrLimit
}
func (c *r2Client) Rotate(ctx context.Context, keep int, preserve string) error {
	if keep <= 0 {
		return nil
	}
	items, err := c.List(ctx)
	if err != nil {
		return err
	}
	kept := 0
	for _, item := range items {
		if item.Key == preserve {
			continue
		}
		kept++
		if kept >= keep {
			if err := c.Delete(ctx, item.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
