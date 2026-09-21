package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
)

// RememberInvocationDiagnosticsReader exposes the admitted-call read model to
// the private control portal. List results never contain capture bodies.
type RememberInvocationDiagnosticsReader interface {
	ListRememberInvocationDiagnostics(context.Context, RememberInvocationDiagnosticFilter) (*RememberInvocationDiagnosticPage, error)
	GetRememberInvocationDiagnostic(context.Context, string, string) (*RememberInvocationDiagnosticDetail, error)
}

type RememberInvocationDiagnosticFilter struct {
	TeamID             string
	OwnerProfileID     string
	InvocationID       string
	CanonicalAttemptID string
	RequestHash        string
	CorrelationID      string
	Classification     string
	Outcome            string
	Retryable          *bool
	Limit              int
	Offset             int
}

type RememberInvocationDiagnosticPage struct {
	Items []RememberInvocationDiagnosticSummary
	Total int64
}

type RememberInvocationDiagnosticSummary struct {
	TeamID              string     `json:"team_id"`
	OwnerProfileID      string     `json:"owner_profile_id"`
	InvocationID        string     `json:"invocation_id"`
	CanonicalAttemptID  string     `json:"canonical_attempt_id,omitempty"`
	RequestHash         string     `json:"request_hash"`
	CorrelationID       string     `json:"correlation_id"`
	Classification      string     `json:"classification"`
	Outcome             string     `json:"outcome"`
	Phase               string     `json:"phase"`
	ProtectedCause      string     `json:"protected_cause,omitempty"`
	DeliveryStage       string     `json:"delivery_stage"`
	FailedPhase         string     `json:"failed_phase,omitempty"`
	ErrorCode           string     `json:"error_code,omitempty"`
	Retryable           bool       `json:"retryable"`
	DurationMS          int64      `json:"duration_ms"`
	CreatedAt           time.Time  `json:"created_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	ExpiresAt           time.Time  `json:"expires_at"`
	RetainedByLegalHold bool       `json:"retained_by_legal_hold"`
}

type RememberInvocationDiagnosticExchange struct {
	DiagnosticID        string    `json:"diagnostic_id,omitempty"`
	SequenceNo          int       `json:"sequence_no"`
	Kind                string    `json:"kind"`
	Component           string    `json:"component"`
	Model               string    `json:"model,omitempty"`
	RequestBody         string    `json:"request_body,omitempty"`
	ResponseBody        string    `json:"response_body,omitempty"`
	RequestContentType  string    `json:"request_content_type,omitempty"`
	ResponseContentType string    `json:"response_content_type,omitempty"`
	StatusCode          int       `json:"status_code,omitempty"`
	Outcome             string    `json:"outcome"`
	CaptureState        string    `json:"capture_state"`
	CaptureReason       string    `json:"capture_reason,omitempty"`
	CapturedAt          time.Time `json:"captured_at,omitempty"`
	ExpiresAt           time.Time `json:"expires_at,omitempty"`
}

type RememberInvocationDiagnosticDetail struct {
	RememberInvocationDiagnosticSummary
	RequestBody                 string                                 `json:"request_body,omitempty"`
	RequestCaptureState         string                                 `json:"request_capture_state"`
	RequestCaptureReason        string                                 `json:"request_capture_reason,omitempty"`
	ProviderExchanges           []RememberInvocationDiagnosticExchange `json:"provider_exchanges"`
	CallerResponse              string                                 `json:"caller_response,omitempty"`
	CallerResponseCaptureState  string                                 `json:"caller_response_capture_state"`
	CallerResponseCaptureReason string                                 `json:"caller_response_capture_reason,omitempty"`
	EnrichmentUnavailable       bool                                   `json:"enrichment_unavailable,omitempty"`
}

var ErrRememberInvocationDiagnosticsUnavailable = fmt.Errorf("remember invocation diagnostics unavailable")
var ErrRememberInvocationDiagnosticNotFound = knowledgecontract.ErrRememberInvocationDiagnosticNotFound

type RememberInvocationDiagnosticsService struct {
	repo remembercontract.RememberInvocationDiagnosticsRepository
}

func NewRememberInvocationDiagnosticsService(repo remembercontract.RememberInvocationDiagnosticsRepository) *RememberInvocationDiagnosticsService {
	return &RememberInvocationDiagnosticsService{repo: repo}
}

func (s *RememberInvocationDiagnosticsService) ListRememberInvocationDiagnostics(ctx context.Context, filter RememberInvocationDiagnosticFilter) (*RememberInvocationDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRememberInvocationDiagnosticsUnavailable
	}
	normalized, err := normalizeRememberInvocationDiagnosticFilter(filter)
	if err != nil {
		return nil, err
	}
	page, err := s.repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: normalized.TeamID, OwnerProfileID: normalized.OwnerProfileID,
		InvocationID: normalized.InvocationID, CanonicalAttemptID: normalized.CanonicalAttemptID,
		RequestHash: normalized.RequestHash, CorrelationID: normalized.CorrelationID,
		Classification: normalized.Classification, Outcome: normalized.Outcome,
		Retryable: normalized.Retryable, Limit: normalized.Limit, Offset: normalized.Offset,
	})
	if err != nil {
		return nil, ErrRememberInvocationDiagnosticsUnavailable
	}
	result := &RememberInvocationDiagnosticPage{Items: []RememberInvocationDiagnosticSummary{}}
	if page == nil {
		return result, nil
	}
	result.Total = page.Total
	for _, record := range page.Records {
		result.Items = append(result.Items, rememberInvocationDiagnosticSummary(record))
	}
	return result, nil
}

func (s *RememberInvocationDiagnosticsService) GetRememberInvocationDiagnostic(ctx context.Context, teamID, invocationID string) (*RememberInvocationDiagnosticDetail, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRememberInvocationDiagnosticsUnavailable
	}
	teamID, invocationID = strings.TrimSpace(teamID), strings.TrimSpace(invocationID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(invocationID); err != nil {
		return nil, fmt.Errorf("invocation_id must be a UUID: %w", err)
	}
	record, err := s.repo.GetRememberInvocationDiagnostic(ctx, teamID, invocationID)
	if errors.Is(err, knowledgecontract.ErrRememberInvocationDiagnosticNotFound) {
		return nil, ErrRememberInvocationDiagnosticNotFound
	}
	if err != nil || record == nil {
		return nil, ErrRememberInvocationDiagnosticsUnavailable
	}
	return rememberInvocationDiagnosticDetail(*record), nil
}

func normalizeRememberInvocationDiagnosticFilter(filter RememberInvocationDiagnosticFilter) (RememberInvocationDiagnosticFilter, error) {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	filter.OwnerProfileID = strings.TrimSpace(filter.OwnerProfileID)
	filter.InvocationID = strings.TrimSpace(filter.InvocationID)
	filter.CanonicalAttemptID = strings.TrimSpace(filter.CanonicalAttemptID)
	filter.RequestHash = strings.TrimSpace(filter.RequestHash)
	filter.CorrelationID = strings.TrimSpace(filter.CorrelationID)
	filter.Classification = strings.TrimSpace(filter.Classification)
	filter.Outcome = strings.TrimSpace(filter.Outcome)
	for name, value := range map[string]string{"team_id": filter.TeamID, "owner_profile_id": filter.OwnerProfileID, "invocation_id": filter.InvocationID, "canonical_attempt_id": filter.CanonicalAttemptID} {
		if value != "" {
			if _, err := uuid.Parse(value); err != nil {
				return RememberInvocationDiagnosticFilter{}, fmt.Errorf("%s must be a UUID: %w", name, err)
			}
		}
	}
	switch filter.Classification {
	case "", "execution", "replay", "conflict":
	default:
		return RememberInvocationDiagnosticFilter{}, fmt.Errorf("classification is unsupported")
	}
	switch filter.Outcome {
	case "", "completed", "evaluated_zero", "failed", "cancelled", "replayed", "conflict":
	default:
		return RememberInvocationDiagnosticFilter{}, fmt.Errorf("outcome is unsupported")
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter, nil
}

func rememberInvocationDiagnosticSummary(record knowledgecontract.RememberInvocationDiagnosticRecord) RememberInvocationDiagnosticSummary {
	var completedAt *time.Time
	if !record.CompletedAt.IsZero() {
		value := record.CompletedAt.UTC()
		completedAt = &value
	}
	return RememberInvocationDiagnosticSummary{
		TeamID: record.TeamID, OwnerProfileID: record.OwnerProfileID, InvocationID: record.InvocationID,
		CanonicalAttemptID: record.CanonicalAttemptID, RequestHash: record.RequestHash, CorrelationID: record.CorrelationID,
		Classification: record.Classification, Outcome: record.Outcome, FailedPhase: record.FailedPhase,
		Phase: record.FailedPhase, DeliveryStage: "unknown_receipt",
		ErrorCode: record.ErrorCode, Retryable: record.Retryable, DurationMS: record.Duration.Milliseconds(),
		CreatedAt: record.CreatedAt.UTC(), CompletedAt: completedAt, ExpiresAt: record.ExpiresAt.UTC(),
		RetainedByLegalHold: record.RetainedByLegalHold,
	}
}

func rememberInvocationDiagnosticDetail(record knowledgecontract.RememberInvocationDiagnosticRecord) *RememberInvocationDiagnosticDetail {
	result := &RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberInvocationDiagnosticSummary(record),
		RequestBody:                         string(record.RequestBody), RequestCaptureState: record.RequestCaptureState,
		RequestCaptureReason: record.RequestCaptureReason, CallerResponse: string(record.CallerResponse),
		CallerResponseCaptureState:  record.CallerResponseCaptureState,
		CallerResponseCaptureReason: record.CallerResponseCaptureReason,
		ProviderExchanges:           make([]RememberInvocationDiagnosticExchange, 0, len(record.ProviderExchanges)),
	}
	for _, exchange := range record.ProviderExchanges {
		capturedAt := exchange.CapturedAt
		if capturedAt.IsZero() {
			capturedAt = record.CreatedAt
		}
		result.ProviderExchanges = append(result.ProviderExchanges, RememberInvocationDiagnosticExchange{
			DiagnosticID: exchange.DiagnosticID, SequenceNo: exchange.SequenceNo, Kind: exchange.Kind,
			Component: exchange.Component, Model: exchange.Model, RequestBody: string(exchange.RequestBody),
			ResponseBody: string(exchange.ResponseBody), RequestContentType: exchange.RequestContentType,
			ResponseContentType: exchange.ResponseContentType, StatusCode: exchange.StatusCode,
			Outcome: exchange.Outcome, CaptureState: exchange.CaptureState, CaptureReason: exchange.CaptureReason,
			CapturedAt: capturedAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(),
		})
	}
	return result
}
