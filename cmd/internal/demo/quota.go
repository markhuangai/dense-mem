package demo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const (
	defaultSessionTTL = 24 * time.Hour
	defaultCounterTTL = 25 * time.Hour
)

type CounterStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

type Quotas struct {
	SessionTTL        time.Duration
	CounterTTL        time.Duration
	TotalRequests     int64
	WriteAttempts     int64
	EvidenceAttempts  int64
	EvidenceBytes     int64
	VerifierAttempts  int64
	RecallCalls       int64
	PerMinuteRequests int
	IssuePerIPDay     int64
}

type QuotaLimits struct {
	SessionHours      int   `json:"session_hours"`
	TotalRequests     int64 `json:"total_requests"`
	WriteAttempts     int64 `json:"write_attempts"`
	EvidenceAttempts  int64 `json:"evidence_attempts"`
	EvidenceBytes     int64 `json:"evidence_bytes"`
	VerifierAttempts  int64 `json:"verifier_attempts"`
	RecallCalls       int64 `json:"recall_calls"`
	PerMinuteRequests int   `json:"per_minute_requests"`
}

func DefaultQuotas() Quotas {
	return Quotas{
		SessionTTL:        defaultSessionTTL,
		CounterTTL:        defaultCounterTTL,
		TotalRequests:     300,
		WriteAttempts:     75,
		EvidenceAttempts:  30,
		EvidenceBytes:     128 * 1024,
		VerifierAttempts:  30,
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
	if q.EvidenceAttempts <= 0 {
		q.EvidenceAttempts = defaults.EvidenceAttempts
	}
	if q.EvidenceBytes <= 0 {
		q.EvidenceBytes = defaults.EvidenceBytes
	}
	if q.VerifierAttempts <= 0 {
		q.VerifierAttempts = defaults.VerifierAttempts
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
		EvidenceAttempts:  q.EvidenceAttempts,
		EvidenceBytes:     q.EvidenceBytes,
		VerifierAttempts:  q.VerifierAttempts,
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

func (m *QuotaManager) ConsumeMemoryEvidence(ctx context.Context, teamID string, count int64, bytes int64) error {
	if count <= 0 {
		count = 1
	}
	if err := m.consume(ctx, teamID, "evidence", "memory evidence items", count, m.Quotas().EvidenceAttempts); err != nil {
		return err
	}
	if bytes <= 0 {
		return nil
	}
	return m.consume(ctx, teamID, "evidence_bytes", "memory evidence bytes", bytes, m.Quotas().EvidenceBytes)
}

func (m *QuotaManager) ConsumeVerifier(ctx context.Context, teamID string, count int64) error {
	if count <= 0 {
		count = 1
	}
	return m.consume(ctx, teamID, "verifier", "semantic verifier attempts", count, m.Quotas().VerifierAttempts)
}

func (m *QuotaManager) ConsumeRecall(ctx context.Context, teamID string) error {
	return m.consume(ctx, teamID, "recall", "recall calls", 1, m.Quotas().RecallCalls)
}

func (m *QuotaManager) consume(ctx context.Context, teamID, counter, label string, delta, limit int64) error {
	if m == nil || m.store == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo quota store unavailable")
	}
	if strings.TrimSpace(teamID) == "" {
		return httperr.New(httperr.FORBIDDEN, "demo team is required")
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
			teamID := ""
			if actor, ok := requestctx.ActorProfileFromContext(c.Request().Context()); ok && actor.TeamID != uuid.Nil {
				teamID = actor.TeamID.String()
			}
			if teamID == "" {
				if resolved, ok := httpmw.GetResolvedProfileID(c.Request().Context()); ok && resolved != uuid.Nil {
					teamID = resolved.String()
				}
			}
			if teamID == "" {
				if principal := httpmw.GetPrincipal(c.Request().Context()); principal != nil && principal.GetTeamID() != uuid.Nil {
					teamID = principal.GetTeamID().String()
				}
			}
			if teamID != "" {
				if err := manager.ConsumeRequest(c.Request().Context(), teamID); err != nil {
					return err
				}
			}
			return next(c)
		}
	}
}

func WrapRegistry(reg registry.Registry, manager *QuotaManager) (registry.Registry, error) {
	wrapped := registry.New()
	for _, tool := range reg.List() {
		invoke := tool.Invoke
		name := tool.Name
		if invoke != nil {
			tool.Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				teamID := toolTeamID(ctx, profileID)
				if err := manager.ConsumeTool(ctx, teamID, name, input); err != nil {
					return nil, err
				}
				return invoke(ctx, profileID, input)
			}
		}
		if err := wrapped.Register(tool); err != nil {
			return nil, err
		}
	}
	return wrapped, nil
}

func (m *QuotaManager) ConsumeTool(ctx context.Context, teamID, toolName string, input map[string]any) error {
	switch toolName {
	case registry.ToolRemember:
		count, bytes := rememberEvidenceUsage(input)
		if err := m.ConsumeWrite(ctx, teamID); err != nil {
			return err
		}
		if err := m.ConsumeMemoryEvidence(ctx, teamID, count, bytes); err != nil {
			return err
		}
		return m.ConsumeVerifier(ctx, teamID, count)
	case registry.ToolRecallMemory:
		return m.ConsumeRecall(ctx, teamID)
	case registry.ToolResolveMemoryPlacement,
		registry.ToolCorrectEntityResolution,
		registry.ToolResolveDreamFeedback,
		registry.ToolImportMemoryPack,
		registry.ToolRollbackMemoryPackImport:
		return m.ConsumeWrite(ctx, teamID)
	default:
		return nil
	}
}

func toolTeamID(ctx context.Context, profileID string) string {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String()
	}
	return strings.TrimSpace(profileID)
}

func rememberEvidenceUsage(input map[string]any) (int64, int64) {
	raw, ok := input["evidence"].([]any)
	if !ok {
		if typed, ok := input["evidence"].([]map[string]any); ok {
			raw = make([]any, 0, len(typed))
			for _, item := range typed {
				raw = append(raw, item)
			}
		}
	}
	var bytes int64
	for _, item := range raw {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := fields["content"].(string); ok {
			bytes += int64(len([]byte(content)))
		}
	}
	return int64(len(raw)), bytes
}
