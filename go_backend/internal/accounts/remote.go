package accounts

import (
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/upstream"
	"context"
	"errors"
)

type UpstreamRemote struct{ Policy config.Upstream }

func (r UpstreamRemote) Sync(ctx context.Context, c Credential) (map[string]any, error) {
	s, err := upstream.NewSession(r.Policy, c.Proxy)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	info, err := s.AccountInfo(ctx, c.AccessToken)
	if err != nil {
		var failure *upstream.Failure
		if errors.As(err, &failure) && failure.Status == 401 {
			return nil, ErrRemoteInvalid
		}
		return nil, err
	}
	status := "正常"
	if !info.Unknown && info.Quota == 0 {
		status = "限流"
	}
	result := map[string]any{"email": info.Email, "user_id": info.UserID, "quota": info.Quota, "image_quota_unknown": info.Unknown, "restore_at": info.RestoreAt, "limits_progress": info.Limits, "default_model_slug": info.DefaultModel, "status": status}
	if info.Plan != "" {
		result["type"] = info.Plan
	}
	return result, nil
}
func (r UpstreamRemote) Refresh(ctx context.Context, c Credential) (Credential, error) {
	tokens, err := upstream.RefreshOAuth(ctx, r.Policy, c.Proxy, c.RefreshToken)
	if errors.Is(err, upstream.ErrOAuthTerminal) {
		return Credential{}, ErrRefreshTerminal
	}
	if err != nil {
		return Credential{}, err
	}
	return Credential{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, Proxy: c.Proxy}, nil
}
