package images

import (
	"context"
	"strings"

	"chatgpt2api/go_backend/internal/managedhttp"
)

// ResolvePublicBaseURL 统一图片输出来源：YAML、业务设置、请求实际地址。
// 仅解析公开地址，不建立网络连接；错误不携带原始配置。
func (s *ImageStorageService) ResolvePublicBaseURL(ctx context.Context, fallback string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	startup := s.PublicBaseURL
	s.mu.Unlock()
	if startup != "" {
		return publicBaseURL(startup)
	}
	values, err := s.runtimeSettings(ctx)
	if err != nil {
		return "", err
	}
	if value, ok := values["base_url"].(string); ok && strings.TrimSpace(value) != "" {
		return publicBaseURL(value)
	}
	return publicBaseURL(fallback)
}

func publicBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	u, err := managedhttp.ParseURL(raw)
	if err != nil || strings.Contains(raw, "#") {
		return "", ErrStorageConfig
	}
	return u.String(), nil
}
