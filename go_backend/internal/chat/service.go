package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/go_backend/internal/accounts"
	"chatgpt2api/go_backend/internal/auth"
	"chatgpt2api/go_backend/internal/callrecord"
	"chatgpt2api/go_backend/internal/config"
	"chatgpt2api/go_backend/internal/upstream"
)

var ErrBusy = errors.New("当前文本调用容量已满，请稍后重试")
var ErrDelivery = errors.New("响应交付失败")

type AccountRepository interface {
	Select(context.Context, string) (accounts.Account, error)
}
type RecordRepository interface {
	Start(context.Context, callrecord.Record) error
	Finish(context.Context, callrecord.Record) error
}
type Session interface {
	Converse(context.Context, string, []upstream.Message, string, string, func(string) error) (upstream.ConversationResult, error)
	Close()
}
type SessionFactory func(config.Upstream, string) (Session, error)
type Service struct {
	accounts AccountRepository
	records  RecordRepository
	policy   config.Upstream
	factory  SessionFactory
	active   chan struct{}
}

func New(accounts AccountRepository, records RecordRepository, policy config.Upstream, factory SessionFactory) *Service {
	if factory == nil {
		factory = func(policy config.Upstream, proxy string) (Session, error) { return upstream.NewSession(policy, proxy) }
	}
	return &Service{accounts: accounts, records: records, policy: policy, factory: factory, active: make(chan struct{}, 32)}
}

func (s *Service) ExecutionCapacity() int { return cap(s.active) }

type Result struct {
	ID           string
	Created      int64
	Model        string
	Conversation upstream.ConversationResult
	Search       upstream.SearchResult
}

// Execute 是一次调用的唯一生命周期所有者；无自动重试、换号或无界排队。
// 开始记录成功后才发上游请求；所有终态都尝试在独立短预算内收口记录。
func (s *Service) Execute(parent context.Context, subject auth.Subject, request Request, emit func(context.Context, Result, string) error, complete func(context.Context, Result) error) (Result, error) {
	return s.execute(parent, subject, request, s.policy.AccountID, emit, complete)
}
func (s *Service) ExecuteForAccount(parent context.Context, subject auth.Subject, request Request, id string, emit func(context.Context, Result, string) error, complete func(context.Context, Result) error) (Result, error) {
	return s.execute(parent, subject, request, id, emit, complete)
}
func (s *Service) execute(parent context.Context, subject auth.Subject, request Request, accountID string, emit func(context.Context, Result, string) error, complete func(context.Context, Result) error) (result Result, err error) {
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		if observer, ok := s.records.(interface{ Rejected() }); ok {
			observer.Rejected()
		}
		return result, ErrBusy
	}
	budget := s.policy.TextTimeoutSeconds
	if budget < 1 || budget > 86400 || s.accounts == nil || s.records == nil {
		return result, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(budget)*time.Second)
	defer cancel()
	request.UpstreamModel, err = executionModel(request.Model, request.Effort)
	if err != nil {
		return result, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, ErrUnavailable
	}
	started := time.Now()
	id := hex.EncodeToString(random[:])
	result = Result{ID: "chatcmpl-" + id, Created: started.Unix(), Model: request.Model}
	record := callrecord.Record{ID: "call-" + id, Subject: subject, Started: started, Model: request.Model, Effort: request.Effort, Stage: "account_selection", Endpoint: request.Endpoint}
	inputText := make([]string, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Content != "" {
			inputText = append(inputText, message.Role+":\n"+message.Content)
		}
	}
	record.RequestText, record.RequestTextTruncated = callrecord.CaptureRequestText(strings.Join(inputText, "\n\n"))
	imageCount := request.ReferenceImageCount
	for _, message := range request.Messages {
		imageCount += len(message.Images)
	}
	record.Trace = callrecord.NewTrace(callrecord.RequestMetadata{Stream: request.Stream}, map[string]int{"messages": len(request.Messages), "images": imageCount})
	ctx = callrecord.WithTrace(ctx, record.Trace)
	attemptCtx := ctx
	record.UpstreamModel = request.UpstreamModel
	if request.Search {
		record.UpstreamModel = "gpt-5-5"
	}
	if err = s.records.Start(ctx, record); err != nil {
		return result, callrecord.ErrStorage
	}
	var lease *accounts.Lease
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		record.Ended = time.Now()
		record.Outcome = "success"
		if err != nil {
			record.Outcome = "failed"
			if errors.Is(err, context.Canceled) {
				record.Outcome = "cancelled"
			}
			record.ErrorCode = PublicError(err).Code
			var failure *upstream.Failure
			if errors.As(err, &failure) {
				record.Stage = failure.Stage
			}
		} else {
			record.Stage = "completed"
		}
		record.ConversationID = result.Conversation.ConversationID
		record.MessageID = result.Conversation.MessageID
		record.ResolvedModel = result.Conversation.ResolvedModel
		finish, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer cancel()
		if lease != nil {
			if e := lease.Finish(finish, err == nil, 0, err); e != nil {
				err = e
				record.Outcome = "failed"
				record.ErrorCode = "account_storage_unavailable"
			}
		}
		if err != nil {
			record.PublicError = PublicError(err).Message
		}
		if lease != nil {
			d := callrecord.AttemptResult{Status: record.Outcome, ErrorCode: record.ErrorCode, Stage: record.Stage, ConversationID: record.ConversationID, EndedAt: record.Ended}
			if err != nil {
				d.PublicError = PublicError(err).Message
			}
			record.Trace.EndAttempt(attemptCtx, d)
		}
		if finishErr := s.records.Finish(finish, record); finishErr != nil {
			err = callrecord.ErrStorage
		}
	}()
	selectionStarted := time.Now()
	selectionDone := callrecord.Span(ctx, "account_selection", "account_wait_ms")
	lease, err = accounts.Acquire(ctx, s.accounts, accountID, false)
	selectionDone(err)
	if err != nil {
		return result, err
	}
	account := lease.Account
	attemptCtx = record.Trace.BeginAttempt(ctx, 1, 1, account.ID, account.Email, "", account.Proxy, selectionStarted)
	ctx = attemptCtx
	record.Attempts = 1
	record.AccountID = account.ID
	record.AccountEmail = account.Email
	progress := func() {
		if observer, ok := s.records.(interface{ Progress(callrecord.Record) }); ok {
			observer.Progress(record)
		}
	}
	policy := s.policy
	policy.TotalTimeoutSeconds = budget
	session, err := s.factory(policy, account.Proxy)
	if err != nil {
		return result, ErrUnavailable
	}
	defer session.Close()
	record.Stage = "upstream"
	progress()
	if request.Search {
		search, ok := session.(interface {
			Search(context.Context, string, string) (upstream.SearchResult, error)
		})
		if !ok {
			return result, ErrUnavailable
		}
		query := ""
		for _, m := range request.Messages {
			if m.Role == "user" {
				query = m.Content
			}
		}
		result.Search, err = search.Search(ctx, account.AccessToken, query)
		result.Conversation = upstream.ConversationResult{ConversationID: result.Search.ConversationID, MessageID: result.Search.AssistantMessageID, ResolvedModel: "gpt-5-5"}
	} else {
		result.Conversation, err = session.Converse(ctx, account.AccessToken, request.Messages, request.UpstreamModel, request.Effort, func(delta string) error {
			record.Stage = "delivery"
			progress()
			if emit == nil || emit(ctx, result, delta) != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrDelivery
			}
			record.Stage = "upstream"
			progress()
			return nil
		})
	}
	if err == nil && complete != nil {
		deliveryDone := callrecord.Span(ctx, "delivery", "response_ms")
		record.Stage = "delivery"
		progress()
		if complete(ctx, result) != nil {
			err = ErrDelivery
		}
		deliveryDone(err)
	}
	return result, err
}

type Error struct {
	Status        int
	Code, Message string
}

func PublicError(err error) Error {
	var input *InputError
	var failure *upstream.Failure
	switch {
	case errors.As(err, &input):
		return Error{400, "invalid_request", input.Message}
	case errors.Is(err, ErrBusy), errors.Is(err, accounts.ErrBusy):
		return Error{429, "text_capacity_exceeded", ErrBusy.Error()}
	case errors.Is(err, ErrDelivery):
		return Error{502, "downstream_delivery_failed", ErrDelivery.Error()}
	case errors.Is(err, accounts.ErrSelection):
		return Error{503, "upstream_account_unavailable", accounts.ErrSelection.Error()}
	case errors.Is(err, accounts.ErrStorage):
		return Error{503, "account_storage_unavailable", accounts.ErrStorage.Error()}
	case errors.Is(err, callrecord.ErrStorage):
		return Error{503, "call_record_unavailable", callrecord.ErrStorage.Error()}
	case errors.Is(err, context.Canceled):
		return Error{499, "request_cancelled", "请求已取消"}
	case errors.Is(err, context.DeadlineExceeded):
		return Error{504, "upstream_timeout", "上游文本调用超时"}
	case errors.As(err, &failure):
		status := 502
		if failure.Status == 429 {
			status = 429
		}
		return Error{status, failure.Code, "上游拒绝请求，阶段：" + failure.Stage + "（HTTP " + strconv.Itoa(failure.Status) + "）"}
	case errors.Is(err, upstream.ErrChallenge):
		return Error{502, "upstream_challenge_failed", "上游验证挑战未能完成"}
	case errors.Is(err, upstream.ErrProtocol):
		return Error{502, "upstream_protocol_error", "上游文本响应未正常结束或格式不受支持"}
	case errors.Is(err, upstream.ErrResponseTooLarge):
		return Error{502, "upstream_response_too_large", "上游响应超过字节限制"}
	default:
		return Error{502, "upstream_unavailable", "上游文本服务暂不可用"}
	}
}
