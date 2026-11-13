package updates

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const releaseAPI = "https://api.github.com/repos/Drunkard-baifeng/go-chatgpt2api/releases/latest"
const maxPackage int64 = 256 << 20
const maxExpanded int64 = 512 << 20

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type release struct {
	Tag        string  `json:"tag_name"`
	Body       string  `json:"body"`
	Published  string  `json:"published_at"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
	Date       string  `json:"-"`
}

var stableVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func versionParts(value string) ([3]uint64, bool) {
	var parts [3]uint64
	match := stableVersion.FindStringSubmatch(value)
	if len(match) != 4 || len(value) > 64 {
		return parts, false
	}
	for i := range parts {
		var err error
		parts[i], err = strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return parts, false
		}
	}
	return parts, true
}

func newer(candidate, current string) bool {
	a, aOK := versionParts(candidate)
	b, bOK := versionParts(current)
	if !aOK || !bOK {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func trustedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || len(raw) > 8192 {
		return false
	}
	switch u.Host {
	case "api.github.com":
		return u.Path == "/repos/Drunkard-baifeng/go-chatgpt2api/releases/latest" && u.RawQuery == ""
	case "github.com":
		return strings.HasPrefix(u.Path, "/Drunkard-baifeng/go-chatgpt2api/releases/download/") && u.RawQuery == ""
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

// 发布源固定，实际拨号也固定到经过检查的公网 DNS 结果；不读取环境代理。
func newClient() *http.Client {
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || !trustedHost(host) {
			return nil, errors.New("发布连接目标无效")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("发布服务 DNS 查询失败")
		}
		for _, ip := range ips {
			ip = ip.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
				return nil, errors.New("发布服务解析到了不允许的地址")
			}
		}
		for _, ip := range ips {
			connection, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, errors.New("无法连接发布服务")
	}
	return &http.Client{Transport: transport, Timeout: 8 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !trustedURL(req.URL.String()) {
			return errors.New("发布下载重定向目标无效")
		}
		return nil
	}}
}

func trustedHost(host string) bool {
	switch host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

func request(ctx context.Context, client *http.Client, raw string) (*http.Response, error) {
	if !trustedURL(raw) {
		return nil, errors.New("发布包下载来源无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("发布请求无效")
	}
	req.Header.Set("User-Agent", "go-chatgpt2api-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接 GitHub 发布服务，请检查网络后重试")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		if response.StatusCode == 403 || response.StatusCode == 429 {
			return nil, errors.New("GitHub 发布查询受限，请稍后重试")
		}
		if response.StatusCode == 404 {
			return nil, errors.New("尚无可用的正式发布包")
		}
		return nil, fmt.Errorf("发布服务返回 HTTP %d，请稍后重试", response.StatusCode)
	}
	return response, nil
}

func readRemote(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, error) {
	response, err := request(ctx, client, raw)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, errors.New("发布响应超过大小限制")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("发布响应读取失败或超过大小限制")
	}
	return data, nil
}

func fetchRelease(ctx context.Context, client *http.Client) (release, error) {
	var r release
	data, err := readRemote(ctx, client, releaseAPI, 1<<20)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(data, &r) != nil {
		return r, errors.New("发布服务返回了无效的版本信息")
	}
	if _, ok := versionParts(r.Tag); !ok || r.Draft || r.Prerelease {
		return release{}, errors.New("发布信息不是受支持的正式稳定版")
	}
	if date, err := time.Parse(time.RFC3339, r.Published); err == nil {
		r.Date = date.UTC().Format("2006-01-02")
	} else {
		r.Date = "发布日期未知"
	}
	return r, nil
}

func (r release) assets() (asset, asset, error) {
	suffix := ".tar.gz"
	if runtime.GOOS == "windows" {
		suffix = ".zip"
	}
	name := "go-chatgpt2api_" + r.Tag + "_" + runtime.GOOS + "_" + runtime.GOARCH + suffix
	var archive, manifest asset
	for _, a := range r.Assets {
		if a.Name != name && a.Name != "SHA256SUMS" {
			continue
		}
		if a.URL != "https://github.com/Drunkard-baifeng/go-chatgpt2api/releases/download/"+r.Tag+"/"+a.Name || a.Size <= 0 || a.Size > maxPackage {
			return asset{}, asset{}, errors.New("发布附件来源或大小无效")
		}
		if a.Name == name {
			if archive.Name != "" {
				return asset{}, asset{}, errors.New("发布包重复")
			}
			archive = a
		} else {
			if manifest.Name != "" || a.Size > 64<<10 {
				return asset{}, asset{}, errors.New("校验清单无效")
			}
			manifest = a
		}
	}
	if archive.Name == "" || manifest.Name == "" {
		return asset{}, asset{}, errors.New("缺少当前平台的发布包或 SHA256SUMS，无法在线安装")
	}
	return archive, manifest, nil
}

func download(ctx context.Context, client *http.Client, a asset, destination string, limit int64) error {
	response, err := request(ctx, client, a.URL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if a.Size <= 0 || a.Size > limit || response.ContentLength > limit {
		return errors.New("发布包超过大小限制")
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("无法保存下载包，请检查程序目录权限")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(response.Body, limit+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != a.Size || n > limit {
		os.Remove(destination)
		return errors.New("发布包下载不完整或超过大小限制")
	}
	if a.Digest != "" && a.Digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		os.Remove(destination)
		return errors.New("发布包与发布平台摘要不匹配")
	}
	return nil
}

func verifyDigest(data []byte, digest string) error {
	if digest == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return errors.New("摘要不匹配")
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxPackage+1))
	if err != nil || n == 0 || n > maxPackage {
		return "", errors.New("程序大小无效")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyPackage(file, name string, manifest []byte) error {
	wanted := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if wanted != "" {
				return errors.New("发布包校验项重复")
			}
			wanted = strings.ToLower(fields[0])
		}
	}
	if data, err := hex.DecodeString(wanted); err != nil || len(data) != sha256.Size {
		return errors.New("发布清单缺少有效的当前平台 SHA256")
	}
	actual, err := fileDigest(file)
	if err != nil || actual != wanted {
		return errors.New("发布包 SHA256 校验失败，未修改当前程序")
	}
	return nil
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "chatgpt2api.exe"
	}
	return "chatgpt2api"
}

func safeArchivePath(name string) bool {
	if name == "./" || name == "." {
		return true
	}
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(name) != ".."
}

func extractProgram(archive, name, destination string) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return errors.New("无法创建更新程序")
	}
	ok := false
	defer func() {
		output.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	found := 0
	copyProgram := func(reader io.Reader, size int64) error {
		found++
		if found != 1 || size <= 0 || size > maxPackage {
			return errors.New("压缩包中的程序重复或大小无效")
		}
		n, err := io.Copy(output, io.LimitReader(reader, size+1))
		if err != nil || n != size {
			return errors.New("压缩包中的程序不完整")
		}
		return nil
	}
	if strings.HasSuffix(name, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return errors.New("更新 ZIP 压缩包无效")
		}
		defer z.Close()
		if len(z.File) > 4096 {
			return errors.New("更新压缩包文件过多")
		}
		for _, entry := range z.File {
			if !safeArchivePath(entry.Name) || entry.Mode()&os.ModeSymlink != 0 {
				return errors.New("更新压缩包含不安全路径或链接")
			}
			if strings.TrimPrefix(entry.Name, "./") != executableName() {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return errors.New("无法读取更新程序")
			}
			err = copyProgram(reader, int64(entry.UncompressedSize64))
			reader.Close()
			if err != nil {
				return err
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return errors.New("无法读取更新包")
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return errors.New("更新 GZIP 压缩包无效")
		}
		defer gz.Close()
		bounded := &io.LimitedReader{R: gz, N: maxExpanded + 1}
		tr := tar.NewReader(bounded)
		for count := 0; ; count++ {
			entry, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil || count >= 4096 || bounded.N <= 0 {
				return errors.New("更新 TAR 压缩包无效或超过限制")
			}
			if !safeArchivePath(entry.Name) || (entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeDir) {
				return errors.New("更新压缩包含不安全路径或链接")
			}
			if strings.TrimPrefix(entry.Name, "./") == executableName() {
				if err := copyProgram(tr, entry.Size); err != nil {
					return err
				}
			}
		}
	}
	if found != 1 || output.Sync() != nil || output.Close() != nil {
		return errors.New("更新压缩包缺少完整的当前平台程序")
	}
	ok = true
	return nil
}

func verifyProgram(ctx context.Context, path, tag string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return errors.New("更新包不是可识别的 Go 程序")
	}
	platform := map[string]string{}
	for _, setting := range info.Settings {
		platform[setting.Key] = setting.Value
	}
	if platform["GOOS"] != runtime.GOOS || platform["GOARCH"] != runtime.GOARCH {
		return errors.New("更新程序的系统或架构与当前进程不匹配")
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, path, "--version")
	configureProcess(cmd)
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil || strings.TrimSpace(string(output)) != tag {
		return errors.New("更新程序无法执行或版本与发布标签不一致")
	}
	return nil
}

type boundedOutput []byte

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(*b)+len(p) > 4096 {
		return 0, errors.New("程序输出过多")
	}
	*b = append(*b, p...)
	return len(p), nil
}
