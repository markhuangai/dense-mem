package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

const (
	defaultSessionTTL = 24 * time.Hour
	defaultCounterTTL = 25 * time.Hour
)

// CounterStore is the Redis-backed atomic counter surface used by demo quotas.
type CounterStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

// Quotas contains the per-demo-session limits enforced over the session TTL.
type Quotas struct {
	SessionTTL        time.Duration
	CounterTTL        time.Duration
	TotalRequests     int64
	WriteAttempts     int64
	FragmentAttempts  int64
	FragmentBytes     int64
	CreatedClaims     int64
	VerifierAttempts  int64
	PromotedFacts     int64
	RecallCalls       int64
	PerMinuteRequests int
	IssuePerIPDay     int64
}

// QuotaLimits is the JSON-safe public quota shape returned to the demo page.
type QuotaLimits struct {
	SessionHours      int   `json:"session_hours"`
	TotalRequests     int64 `json:"total_requests"`
	WriteAttempts     int64 `json:"write_attempts"`
	FragmentAttempts  int64 `json:"fragment_attempts"`
	FragmentBytes     int64 `json:"fragment_bytes"`
	CreatedClaims     int64 `json:"created_claims"`
	VerifierAttempts  int64 `json:"verifier_attempts"`
	PromotedFacts     int64 `json:"promoted_facts"`
	RecallCalls       int64 `json:"recall_calls"`
	PerMinuteRequests int   `json:"per_minute_requests"`
}

// DefaultQuotas returns the public demo limits approved for the hosted demo.
func DefaultQuotas() Quotas {
	return Quotas{
		SessionTTL:        defaultSessionTTL,
		CounterTTL:        defaultCounterTTL,
		TotalRequests:     300,
		WriteAttempts:     75,
		FragmentAttempts:  30,
		FragmentBytes:     128 * 1024,
		CreatedClaims:     30,
		VerifierAttempts:  10,
		PromotedFacts:     5,
		RecallCalls:       50,
		PerMinuteRequests: 20,
		IssuePerIPDay:     10,
	}
}

func (q Quotas) normalized() Quotas {
	defaults := DefaultQuotas()
	if q.SessionTTL <= 0 {
		q.SessionTTL = defaults.SessionTTL
	}
	if q.CounterTTL <= 0 {
		q.CounterTTL = defaults.CounterTTL
	}
	if q.TotalRequests <= 0 {
		q.TotalRequests = defaults.TotalRequests
	}
	if q.WriteAttempts <= 0 {
		q.WriteAttempts = defaults.WriteAttempts
	}
	if q.FragmentAttempts <= 0 {
		q.FragmentAttempts = defaults.FragmentAttempts
	}
	if q.FragmentBytes <= 0 {
		q.FragmentBytes = defaults.FragmentBytes
	}
	if q.CreatedClaims <= 0 {
		q.CreatedClaims = defaults.CreatedClaims
	}
	if q.VerifierAttempts <= 0 {
		q.VerifierAttempts = defaults.VerifierAttempts
	}
	if q.PromotedFacts <= 0 {
		q.PromotedFacts = defaults.PromotedFacts
	}
	if q.RecallCalls <= 0 {
		q.RecallCalls = defaults.RecallCalls
	}
	if q.PerMinuteRequests <= 0 {
		q.PerMinuteRequests = defaults.PerMinuteRequests
	}
	if q.IssuePerIPDay <= 0 {
		q.IssuePerIPDay = defaults.IssuePerIPDay
	}
	return q
}

func (q Quotas) Limits() QuotaLimits {
	q = q.normalized()
	return QuotaLimits{
		SessionHours:      int(q.SessionTTL / time.Hour),
		TotalRequests:     q.TotalRequests,
		WriteAttempts:     q.WriteAttempts,
		FragmentAttempts:  q.FragmentAttempts,
		FragmentBytes:     q.FragmentBytes,
		CreatedClaims:     q.CreatedClaims,
		VerifierAttempts:  q.VerifierAttempts,
		PromotedFacts:     q.PromotedFacts,
		RecallCalls:       q.RecallCalls,
		PerMinuteRequests: q.PerMinuteRequests,
	}
}

type QuotaManager struct {
	store  CounterStore
	quotas Quotas
}

func NewQuotaManager(store CounterStore, quotas Quotas) *QuotaManager {
	return &QuotaManager{store: store, quotas: quotas.normalized()}
}

func (m *QuotaManager) Quotas() Quotas {
	if m == nil {
		return DefaultQuotas()
	}
	return m.quotas.normalized()
}

func (m *QuotaManager) Limits() QuotaLimits {
	return m.Quotas().Limits()
}

func (m *QuotaManager) ConsumeRequest(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "requests", "authenticated requests", 1, m.Quotas().TotalRequests)
}

func (m *QuotaManager) ConsumeWrite(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "writes", "write attempts", 1, m.Quotas().WriteAttempts)
}

func (m *QuotaManager) ConsumeFragment(ctx context.Context, teamID string, content string) error {
	if err := m.consume(ctx, teamID, "fragments", "fragment save attempts", 1, m.Quotas().FragmentAttempts); err != nil {
		return err
	}
	return m.consume(ctx, teamID, "fragment_bytes", "stored fragment bytes", int64(len([]byte(content))), m.Quotas().FragmentBytes)
}

func (m *QuotaManager) ConsumeClaim(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "claims", "created claims", 1, m.Quotas().CreatedClaims)
}

func (m *QuotaManager) ConsumeVerifier(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "verifier", "verifier attempts", 1, m.Quotas().VerifierAttempts)
}

func (m *QuotaManager) ConsumeFact(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "facts", "promoted facts", 1, m.Quotas().PromotedFacts)
}

func (m *QuotaManager) ConsumeRecall(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "recall", "recall calls", 1, m.Quotas().RecallCalls)
}

func (m *QuotaManager) consume(ctx context.Context, teamID, counter, label string, delta, limit int64) error {
	if m == nil || m.store == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo quota store unavailable")
	}
	if teamID == "" {
		return httperr.New(httperr.FORBIDDEN, "demo team is required")
	}
	if delta <= 0 {
		return nil
	}
	next, err := m.store.AddWithExpire(ctx, quotaKey(teamID, counter), delta, int64(m.Quotas().CounterTTL.Seconds()))
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo quota check failed")
	}
	if next > limit {
		return httperr.New(httperr.RATE_LIMITED, fmt.Sprintf("demo quota exceeded: %s limit is %d per 24 hours", label, limit))
	}
	return nil
}

func quotaKey(teamID, counter string) string {
	return "demo:quota:" + teamID + ":" + counter
}

func RequestQuotaMiddleware(manager *QuotaManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			teamID, ok := httpmw.GetResolvedProfileID(c.Request().Context())
			if !ok {
				if principal := httpmw.GetPrincipal(c.Request().Context()); principal != nil {
					teamID = principal.GetTeamID()
				}
			}
			if teamID != uuid.Nil {
				if err := manager.ConsumeRequest(c.Request().Context(), teamID.String()); err != nil {
					return err
				}
			}
			return next(c)
		}
	}
}

type quotaFragmentCreateService struct {
	next    fragmentservice.CreateFragmentService
	manager *QuotaManager
}

func WrapFragmentCreate(next fragmentservice.CreateFragmentService, manager *QuotaManager) fragmentservice.CreateFragmentService {
	return quotaFragmentCreateService{next: next, manager: manager}
}

func (s quotaFragmentCreateService) Create(ctx context.Context, teamID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	content := ""
	if req != nil {
		content = req.Content
	}
	if err := s.manager.ConsumeWrite(ctx, teamID); err != nil {
		return nil, err
	}
	if err := s.manager.ConsumeFragment(ctx, teamID, content); err != nil {
		return nil, err
	}
	return s.next.Create(ctx, teamID, req)
}

type quotaClaimCreateService struct {
	next    claimservice.CreateClaimService
	manager *QuotaManager
}

func WrapClaimCreate(next claimservice.CreateClaimService, manager *QuotaManager) claimservice.CreateClaimService {
	return quotaClaimCreateService{next: next, manager: manager}
}

func (s quotaClaimCreateService) Create(ctx context.Context, teamID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	if err := s.manager.ConsumeWrite(ctx, teamID); err != nil {
		return nil, err
	}
	if err := s.manager.ConsumeClaim(ctx, teamID); err != nil {
		return nil, err
	}
	return s.next.Create(ctx, teamID, claim)
}

type quotaClaimVerifyService struct {
	next    claimservice.VerifyClaimService
	manager *QuotaManager
}

func WrapClaimVerify(next claimservice.VerifyClaimService, manager *QuotaManager) claimservice.VerifyClaimService {
	return quotaClaimVerifyService{next: next, manager: manager}
}

func (s quotaClaimVerifyService) Verify(ctx context.Context, teamID string, claimID string) (*domain.Claim, error) {
	if err := s.manager.ConsumeWrite(ctx, teamID); err != nil {
		return nil, err
	}
	if err := s.manager.ConsumeVerifier(ctx, teamID); err != nil {
		return nil, err
	}
	return s.next.Verify(ctx, teamID, claimID)
}

type quotaFactService struct {
	nextPromote factservice.PromoteClaimService
	nextConfirm factservice.ConfirmMemoryService
	manager     *QuotaManager
}

func WrapFactPromote(next factservice.PromoteClaimService, manager *QuotaManager) factservice.PromoteClaimService {
	return quotaFactService{nextPromote: next, manager: manager}
}

func WrapFactConfirm(next factservice.ConfirmMemoryService, manager *QuotaManager) factservice.ConfirmMemoryService {
	return quotaFactService{nextConfirm: next, manager: manager}
}

func (s quotaFactService) Promote(ctx context.Context, teamID string, claimID string) (*domain.Fact, error) {
	if err := s.manager.ConsumeWrite(ctx, teamID); err != nil {
		return nil, err
	}
	if err := s.manager.ConsumeFact(ctx, teamID); err != nil {
		return nil, err
	}
	return s.nextPromote.Promote(ctx, teamID, claimID)
}

func (s quotaFactService) ConfirmMemory(ctx context.Context, teamID string, req factservice.ConfirmMemoryRequest) (*factservice.ConfirmMemoryResult, error) {
	if err := s.manager.ConsumeWrite(ctx, teamID); err != nil {
		return nil, err
	}
	if err := s.manager.ConsumeFact(ctx, teamID); err != nil {
		return nil, err
	}
	return s.nextConfirm.ConfirmMemory(ctx, teamID, req)
}

type quotaRecallService struct {
	next    recallservice.RecallService
	manager *QuotaManager
}

func WrapRecall(next recallservice.RecallService, manager *QuotaManager) recallservice.RecallService {
	return quotaRecallService{next: next, manager: manager}
}

func (s quotaRecallService) Recall(ctx context.Context, teamID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	if err := s.manager.ConsumeRecall(ctx, teamID); err != nil {
		return nil, err
	}
	return s.next.Recall(ctx, teamID, req)
}

type DisabledCommunityDetectService struct{}

func (DisabledCommunityDetectService) Detect(context.Context, string, communityservice.DetectOptions) error {
	return communityservice.ErrCommunityUnavailable
}
