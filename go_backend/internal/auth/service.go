// Package auth 统一识别管理员密钥和用户密钥，不签发额外会话令牌。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	ErrUnauthorized = errors.New("密钥无效或已失效，请重新登录")
	ErrUnavailable  = errors.New("身份服务暂不可用")
)

type Key struct {
	ID, Name, Role, KeyHash string
	Enabled                 bool
}

type Subject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type Capabilities struct {
	AdminConsole bool `json:"admin_console"`
	Studio       bool `json:"studio"`
}

type View struct {
	SchemaVersion int          `json:"schema_version"`
	Authenticated bool         `json:"authenticated"`
	Version       string       `json:"version"`
	Subject       *Subject     `json:"subject"`
	Capabilities  Capabilities `json:"capabilities"`
	HomeRoute     string       `json:"home_route"`
}

type Service struct {
	adminHash [32]byte
	hasAdmin  bool
	lookup    func(context.Context, string) (*Key, error)
	usage     *keyUsage
}

func New(adminKey string, lookup func(context.Context, string) (*Key, error)) *Service {
	adminKey = strings.TrimSpace(adminKey)
	return &Service{adminHash: sha256.Sum256([]byte(adminKey)), hasAdmin: adminKey != "", lookup: lookup}
}

func (s *Service) Authenticate(ctx context.Context, authorization string) (*Subject, error) {
	scheme, token, found := strings.Cut(authorization, " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > 65536 {
		return nil, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	if s.hasAdmin && subtle.ConstantTimeCompare(digest[:], s.adminHash[:]) == 1 {
		return &Subject{ID: "admin", Name: "管理员", Role: "admin"}, nil
	}
	if s.lookup == nil {
		return nil, ErrUnauthorized
	}
	encoded := hex.EncodeToString(digest[:])
	key, err := s.lookup(ctx, encoded)
	if err != nil {
		return nil, ErrUnavailable
	}
	if key == nil || !key.Enabled || key.ID == "" || (key.Role != "admin" && key.Role != "user") || subtle.ConstantTimeCompare([]byte(key.KeyHash), []byte(encoded)) != 1 {
		return nil, ErrUnauthorized
	}
	s.usage.record(ctx, key)
	name := key.Name
	if name == "" {
		name = "普通用户"
		if key.Role == "admin" {
			name = "管理员密钥"
		}
	}
	return &Subject{ID: key.ID, Name: name, Role: key.Role}, nil
}

func BuildView(version string, subject *Subject) View {
	view := View{SchemaVersion: 1, Version: version, HomeRoute: "/login"}
	if subject == nil {
		return view
	}
	copy := *subject
	view.Subject = &copy
	view.Authenticated = true
	view.Capabilities = Capabilities{AdminConsole: subject.Role == "admin", Studio: true}
	view.HomeRoute = "/studio"
	if view.Capabilities.AdminConsole {
		view.HomeRoute = "/"
	}
	return view
}
