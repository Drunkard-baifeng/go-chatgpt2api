package upstream

import (
	"bytes"
	"context"
	"net/http"
	"strings"
)

// DeleteConversation 与 Python 一样隐藏官网历史，不删除本地资产。
func (s *Session) DeleteConversation(ctx context.Context, token, conversation string) error {
	if !safeAssetID.MatchString(conversation) || !safeHeaderToken(token) || strings.TrimSpace(token) == "" {
		return ErrTarget
	}
	p := "/backend-api/conversation/" + conversation
	headers := http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}, "Accept": {"*/*"}, "Origin": {s.origin}, "Referer": {s.origin + "/c/" + conversation}, "X-OpenAI-Target-Route": {"/backend-api/conversation/{conversation_id}"}, "X-OpenAI-Target-Path": {p}}
	response, err := s.Do(ctx, http.MethodPatch, p, bytes.NewBufferString(`{"is_visible":false}`), headers)
	if err != nil {
		return err
	}
	_, err = readResponse(response, "image_cleanup", 1<<20)
	return err
}
