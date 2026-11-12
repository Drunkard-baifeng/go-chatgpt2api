// Package accounts 拥有上游账号、凭据、批量维护和单活动进程账号池。
package accounts

import (
	"chatgpt2api/go_backend/ent"
	"context"
	"errors"
	"regexp"
	"sync"
	"time"
)

var ErrStorage = errors.New("账号仓储暂不可用")
var ErrSelection = errors.New("没有符合条件的可用上游账号")
var ErrInput = errors.New("账号凭据或代理格式无效")
var validID = regexp.MustCompile(`^acct_[a-f0-9]{24}$`)

type Credential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
}
type Account struct {
	Credential
	ID     string
	Email  string
	Source string
	Plan   string
}
type ProxyResolver interface {
	Resolve(context.Context, map[string]any, map[string]any) (string, error)
	AcquireImage(context.Context, map[string]any, map[string]any) (string, func(), error)
}
type Repository struct {
	client           *ent.Client
	timeout          time.Duration
	mu               sync.Mutex
	next             int
	inflight         map[string]int
	active           map[string]int
	maintaining      map[string]int
	processing       int
	jobs             map[string]*Job
	closing          bool
	lifecycleStarted bool
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	runtime          func(context.Context) (map[string]any, error)
	resolve          ProxyResolver
	remote           Remote
}

func New(client *ent.Client, timeout time.Duration) *Repository {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Repository{client: client, timeout: timeout, inflight: map[string]int{}, active: map[string]int{}, maintaining: map[string]int{}, jobs: map[string]*Job{}, ctx: ctx, cancel: cancel}
}
func (c Credential) validate() error {
	if !validateToken(c.AccessToken) || (c.RefreshToken != "" && !validateToken(c.RefreshToken)) || len(c.Proxy) > 4096 || validateReference(c.Proxy) != nil {
		return ErrInput
	}
	return nil
}

type Operation struct {
	Added        int              `json:"added"`
	Skipped      int              `json:"skipped"`
	Synced       int              `json:"synced"`
	Revision     string           `json:"revision"`
	UpdatedIDs   []string         `json:"updated_ids"`
	RemovedIDs   []string         `json:"removed_ids"`
	Errors       []any            `json:"errors"`
	Events       []map[string]any `json:"events"`
	Items        []any            `json:"items"`
	StatusLabel  string           `json:"status_label"`
	Tone         string           `json:"tone"`
	Message      string           `json:"message"`
	SummaryItems []map[string]any `json:"summary_items"`
	Updated      int              `json:"updated"`
	Removed      int              `json:"removed"`
	Refreshed    int              `json:"refreshed"`
	Item         any              `json:"item,omitempty"`
}

// Register 复用批量导入事务；本方法不触发远程维护。
func (r *Repository) Register(ctx context.Context, c Credential) (Operation, error) {
	if err := c.validate(); err != nil {
		return emptyOperation(), err
	}
	return r.Import(ctx, ImportRequest{Accounts: []map[string]any{{"access_token": c.AccessToken, "refresh_token": c.RefreshToken, "proxy": c.Proxy}}})
}

// Select 是无需预留的只读兼容入口，生成链路使用 Acquire。
func (r *Repository) Select(ctx context.Context, id string) (Account, error) {
	lease, err := r.acquire(ctx, id, false, false)
	if err != nil {
		return Account{}, err
	}
	return lease.Account, nil
}
