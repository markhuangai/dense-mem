package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

var (
	ErrSubmissionDiagnosticNotFound     = errors.New("submission diagnostic not found")
	ErrSubmissionDiagnosticsUnavailable = errors.New("submission diagnostics unavailable")
)

type SubmissionDiagnosticsReader interface {
	ListSubmissionDiagnostics(ctx context.Context, filter SubmissionDiagnosticFilter) (*SubmissionDiagnosticPage, error)
	GetSubmissionDiagnostic(ctx context.Context, teamID, submissionID string) (*SubmissionDiagnosticDetail, error)
}

type SubmissionDiagnosticFilter struct {
	TeamID          string
	ProcessingState string
	Limit           int
	Offset          int
}

type SubmissionDiagnosticSummary struct {
	TeamID                 string                               `json:"team_id"`
	TeamName               string                               `json:"team_name"`
	OwnerProfileID         string                               `json:"owner_profile_id"`
	SubmissionID           string                               `json:"submission_id"`
	ProcessingState        string                               `json:"processing_state"`
	CorrelationID          string                               `json:"correlation_id,omitempty"`
	SourceSummary          string                               `json:"source_summary"`
	SourceSummaryTruncated bool                                 `json:"source_summary_truncated"`
	Attempts               int                                  `json:"attempts"`
	MaxAttempts            int                                  `json:"max_attempts"`
	EvidenceCount          int                                  `json:"evidence_count"`
	SubmittedAt            time.Time                            `json:"submitted_at"`
	NextAttemptAt          *time.Time                           `json:"next_attempt_at,omitempty"`
	StartedAt              *time.Time                           `json:"started_at,omitempty"`
	UpdatedAt              *time.Time                           `json:"updated_at,omitempty"`
	CompletedAt            *time.Time                           `json:"completed_at,omitempty"`
	Error                  *memoryservice.SubmissionStatusError `json:"error,omitempty"`
	OperatorDiagnostic     *SubmissionOperatorDiagnostic        `json:"operator_diagnostic,omitempty"`
}

type SubmissionDiagnosticPage struct {
	Items []SubmissionDiagnosticSummary
	Total int64
}

type SubmissionDiagnosticDetail struct {
	memoryservice.SubmissionStatusResult
	TeamID                 string                         `json:"team_id"`
	TeamName               string                         `json:"team_name"`
	OwnerProfileID         string                         `json:"owner_profile_id"`
	EvidenceCount          int                            `json:"evidence_count"`
	SourceSummary          string                         `json:"source_summary"`
	SourceSummaryTruncated bool                           `json:"source_summary_truncated"`
	OperatorDiagnostic     *SubmissionOperatorDiagnostic  `json:"operator_diagnostic,omitempty"`
	OperatorDiagnostics    []SubmissionOperatorDiagnostic `json:"operator_diagnostics"`
}

// SubmissionOperatorDiagnostic is a bounded control-portal-only projection of
// placement assessment failures. It deliberately omits provider responses,
// prompts, evidence content, and storage error text.
type SubmissionOperatorDiagnostic struct {
	ID                      string                        `json:"id,omitempty"`
	PlacementItemID         string                        `json:"placement_item_id,omitempty"`
	OutcomeKind             string                        `json:"outcome_kind,omitempty"`
	Status                  string                        `json:"status,omitempty"`
	OccurredAt              *time.Time                    `json:"occurred_at,omitempty"`
	FailureReasonCode       string                        `json:"failure_reason_code,omitempty"`
	FailureStage            string                        `json:"failure_stage,omitempty"`
	FailureClass            string                        `json:"failure_class,omitempty"`
	ValidationStage         string                        `json:"validation_stage,omitempty"`
	ValidationFieldFamilies []string                      `json:"validation_field_families,omitempty"`
	FailureMeasurement      *SubmissionFailureMeasurement `json:"failure_measurement,omitempty"`
	ProviderStatus          int                           `json:"provider_status,omitempty"`
	AssessorTurns           int                           `json:"assessor_turns,omitempty"`
	ProviderAttempted       bool                          `json:"assessor_provider_attempted,omitempty"`
	Message                 string                        `json:"message,omitempty"`
}

type SubmissionFailureMeasurement struct {
	Unit            string `json:"unit"`
	Observed        int    `json:"observed,omitempty"`
	ObservedAtLeast int    `json:"observed_at_least,omitempty"`
	Limit           int    `json:"limit"`
}

type SubmissionDiagnosticsService struct {
	repo repository.SubmissionDiagnosticsRepository
}

func NewSubmissionDiagnosticsService(repo repository.SubmissionDiagnosticsRepository) *SubmissionDiagnosticsService {
	return &SubmissionDiagnosticsService{repo: repo}
}

func (s *SubmissionDiagnosticsService) ListSubmissionDiagnostics(
	ctx context.Context,
	filter SubmissionDiagnosticFilter,
) (*SubmissionDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	normalized, err := normalizeSubmissionDiagnosticServiceFilter(filter)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.ListSubmissionDiagnostics(ctx, repository.SubmissionDiagnosticFilter{
		TeamID: normalized.TeamID, ProcessingState: normalized.ProcessingState,
		Limit: normalized.Limit, Offset: normalized.Offset,
	})
	if err != nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	page := &SubmissionDiagnosticPage{Items: []SubmissionDiagnosticSummary{}}
	if records == nil {
		return page, nil
	}
	page.Total = records.Total
	page.Items = make([]SubmissionDiagnosticSummary, 0, len(records.Records))
	for index := range records.Records {
		page.Items = append(page.Items, submissionDiagnosticSummary(records.Records[index]))
	}
	return page, nil
}

func (s *SubmissionDiagnosticsService) GetSubmissionDiagnostic(
	ctx context.Context,
	teamID string,
	submissionID string,
) (*SubmissionDiagnosticDetail, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	teamID, submissionID = strings.TrimSpace(teamID), strings.TrimSpace(submissionID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, fmt.Errorf("submission_id must be a UUID: %w", err)
	}
	record, err := s.repo.GetSubmissionDiagnostic(ctx, teamID, submissionID)
	if errors.Is(err, repository.ErrSubmissionDiagnosticNotFound) {
		return nil, ErrSubmissionDiagnosticNotFound
	}
	if err != nil || record == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	status := memoryservice.ProjectSubmissionStatus(&record.Placement)
	sourceSummary := submissionSourceSummary(record.SourceTypes)
	return &SubmissionDiagnosticDetail{
		SubmissionStatusResult: *status,
		TeamID:                 record.Placement.TeamID,
		TeamName:               record.TeamName,
		OwnerProfileID:         record.Placement.OwnerProfileID,
		EvidenceCount:          record.EvidenceCount,
		SourceSummary:          sourceSummary,
		OperatorDiagnostic:     projectSubmissionOperatorDiagnostic(record.OperatorDiagnostic),
		OperatorDiagnostics:    projectSubmissionOperatorDiagnostics(record.OperatorDiagnostics),
	}, nil
}

func submissionDiagnosticSummary(record repository.SubmissionDiagnosticRecord) SubmissionDiagnosticSummary {
	status := memoryservice.ProjectSubmissionStatus(&record.Placement)
	sourceSummary := submissionSourceSummary(record.SourceTypes)
	var statusError *memoryservice.SubmissionStatusError
	if len(status.Errors) > 0 {
		value := status.Errors[0]
		statusError = &value
	}
	submittedAt := time.Time{}
	if record.Placement.SubmittedAt != nil {
		submittedAt = record.Placement.SubmittedAt.UTC()
	}
	return SubmissionDiagnosticSummary{
		TeamID:             record.Placement.TeamID,
		TeamName:           record.TeamName,
		OwnerProfileID:     record.Placement.OwnerProfileID,
		SubmissionID:       record.Placement.IngestID,
		ProcessingState:    status.ProcessingState,
		CorrelationID:      record.Placement.CorrelationID,
		SourceSummary:      sourceSummary,
		Attempts:           record.Placement.Attempts,
		MaxAttempts:        record.Placement.MaxAttempts,
		EvidenceCount:      record.EvidenceCount,
		SubmittedAt:        submittedAt,
		NextAttemptAt:      record.Placement.NextAttemptAt,
		StartedAt:          record.Placement.StartedAt,
		UpdatedAt:          record.Placement.UpdatedAt,
		CompletedAt:        record.Placement.CompletedAt,
		Error:              statusError,
		OperatorDiagnostic: projectSubmissionOperatorDiagnostic(record.OperatorDiagnostic),
	}
}

func submissionSourceSummary(sourceTypes []string) string {
	seen := make(map[domain.SourceType]struct{}, len(sourceTypes))
	for _, sourceType := range sourceTypes {
		typedSource := domain.SourceType(sourceType)
		if typedSource.IsValid() {
			seen[typedSource] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(seen))
	for _, sourceType := range domain.ValidSourceTypes() {
		if _, ok := seen[sourceType]; ok {
			ordered = append(ordered, string(sourceType))
		}
	}
	if len(ordered) == 0 {
		return ""
	}
	return strings.Join(ordered, " + ") + " evidence"
}

func projectSubmissionOperatorDiagnostics(values []repository.SubmissionDiagnosticOperatorDiagnostic) []SubmissionOperatorDiagnostic {
	result := make([]SubmissionOperatorDiagnostic, 0, len(values))
	for _, value := range values {
		if projected, ok := projectSubmissionOperatorDiagnosticRecord(value); ok {
			result = append(result, projected)
		}
	}
	return result
}

func projectSubmissionOperatorDiagnostic(value map[string]any) *SubmissionOperatorDiagnostic {
	if projected, ok := projectSubmissionOperatorDiagnosticPayload(value); ok {
		return &projected
	}
	return nil
}

func projectSubmissionOperatorDiagnosticRecord(value repository.SubmissionDiagnosticOperatorDiagnostic) (SubmissionOperatorDiagnostic, bool) {
	projected, ok := projectSubmissionOperatorDiagnosticPayload(value.Payload)
	if !ok {
		return SubmissionOperatorDiagnostic{}, false
	}
	projected.ID = boundedDiagnosticIdentifier(value.ID)
	projected.PlacementItemID = boundedDiagnosticIdentifier(value.PlacementItemID)
	projected.OutcomeKind = boundedDiagnosticToken(value.OutcomeKind, 96)
	projected.Status = boundedDiagnosticToken(value.Status, 64)
	if !value.CreatedAt.IsZero() {
		occurredAt := value.CreatedAt.UTC()
		projected.OccurredAt = &occurredAt
	}
	return projected, true
}

func projectSubmissionOperatorDiagnosticPayload(value map[string]any) (SubmissionOperatorDiagnostic, bool) {
	if len(value) == 0 {
		return SubmissionOperatorDiagnostic{}, false
	}
	result := SubmissionOperatorDiagnostic{}
	result.FailureReasonCode = allowlistedDiagnosticToken(stringValue(value["failure_reason_code"]), submissionDiagnosticReasonCodes, 96)
	result.FailureStage = allowlistedDiagnosticToken(stringValue(value["failure_stage"]), submissionDiagnosticStages, 64)
	result.FailureClass = allowlistedDiagnosticToken(stringValue(value["failure_class"]), submissionDiagnosticClasses, 64)
	result.ValidationStage = allowlistedDiagnosticToken(stringValue(value["validation_stage"]), submissionDiagnosticValidationStages, 96)
	result.ValidationFieldFamilies = boundedDiagnosticTokens(value["validation_field_families"], 32, 64, submissionDiagnosticFieldFamilies)
	result.ProviderStatus = boundedDiagnosticStatus(value["provider_status"])
	result.AssessorTurns = boundedDiagnosticInt(value["assessor_turns"], 1000)
	result.ProviderAttempted, _ = value["assessor_provider_attempted"].(bool)
	result.FailureMeasurement = projectSubmissionFailureMeasurement(value["failure_measurement"])
	if result.FailureReasonCode == "" && result.FailureStage == "" && result.FailureClass == "" && result.ValidationStage == "" && result.FailureMeasurement == nil {
		return SubmissionOperatorDiagnostic{}, false
	}
	result.Message = submissionOperatorDiagnosticMessage(result)
	return result, true
}

func projectSubmissionFailureMeasurement(value any) *SubmissionFailureMeasurement {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	unit := boundedDiagnosticToken(stringValue(fields["unit"]), 16)
	if unit != "tokens" && unit != "candidates" {
		return nil
	}
	measurement := &SubmissionFailureMeasurement{
		Unit:            unit,
		Observed:        boundedDiagnosticInt(fields["observed"], 100000000),
		ObservedAtLeast: boundedDiagnosticInt(fields["observed_at_least"], 100000000),
		Limit:           boundedDiagnosticInt(fields["limit"], 100000000),
	}
	if measurement.Limit == 0 {
		return nil
	}
	return measurement
}

func submissionOperatorDiagnosticMessage(value SubmissionOperatorDiagnostic) string {
	if value.FailureReasonCode != "" {
		return "placement failure: " + value.FailureReasonCode
	}
	if value.FailureStage != "" && value.FailureClass != "" {
		return "placement failure during " + value.FailureStage + " (" + value.FailureClass + ")"
	}
	if value.ValidationStage != "" {
		return "assessor response validation failed at " + value.ValidationStage
	}
	return "placement failure requires operator review"
}

var (
	submissionDiagnosticReasonCodes = map[string]struct{}{
		"entity_catalog_candidate_limit_exceeded": {},
		"candidate_context_token_limit_exceeded":  {},
		"assessment_input_token_limit_exceeded":   {},
		"predicate_option_limit_exceeded":         {},
		"contract_superseded":                     {},
		"replacement_conflict":                    {},
		"assessor_attempt_consumed":               {},
		"security_quarantine":                     {},
		"semantic_commit_failed":                  {},
		"placement_load_failed":                   {},
		"assessor_response_invalid":               {},
		"assessor_provider_failed":                {},
		"lease_lost":                              {},
		"unknown_internal_failure":                {},
		"repository_persistence_failed":           {},
	}
	submissionDiagnosticStages = map[string]struct{}{
		"entity_catalog": {}, "candidate_prefetch": {}, "catalog_context": {}, "assessment_input": {},
		"catalog_context_validation": {}, "trusted_context_validation": {},
		"predicate_options_overflow": {}, "placement_load": {}, "assessment": {},
		"assessment_attempt_consumed": {}, "confidence_policy": {}, "policy_review": {},
		"deterministic_policy": {}, "commit_review": {}, "conflict_context_stale": {},
		"semantic_commit": {}, "contract_superseded": {}, "replacement_conflict": {},
		"stale_source": {}, "deterministic_security_scan": {}, "security_signal": {},
		"assessment_scope": {}, "review_override": {}, "placement_item": {},
	}
	submissionDiagnosticClasses = map[string]struct{}{
		"timeout": {}, "rate_limited": {}, "http_4xx": {}, "http_5xx": {},
		"http_unexpected": {}, "transport": {}, "provider_protocol": {},
		"provider_unavailable": {}, "request_invalid": {}, "malformed_response": {},
		"malformed_exhausted": {}, "input_budget": {}, "validation_failed": {},
		"internal": {}, "repository": {}, "lease_lost": {}, "canceled": {}, "deadline": {},
	}
	submissionDiagnosticValidationStages = map[string]struct{}{
		"response_json": {}, "response_contract": {}, "response_output_tokens": {},
		"conversation_input_tokens": {}, "stored_response": {},
	}
	submissionDiagnosticFieldFamilies = map[string]struct{}{
		"input_tokens": {}, "request_id": {}, "output_tokens": {}, "response": {}, "other": {},
		"security_signals": {}, "entity_results": {}, "entity_results.ref": {},
		"entity_results.span": {}, "entity_results.kind": {}, "entity_results.selection": {},
		"entity_results.quality": {}, "entity_results.other": {}, "relationship_results": {},
		"relationship_results.ref": {}, "relationship_results.subject": {},
		"relationship_results.predicate": {}, "relationship_results.object": {},
		"relationship_results.evidence": {}, "relationship_results.semantics": {},
		"relationship_results.verdict": {}, "relationship_results.temporal": {},
		"relationship_results.scope": {}, "relationship_results.quality": {},
		"relationship_results.other": {},
	}
)

func allowlistedDiagnosticToken(value string, allowed map[string]struct{}, maxRunes int) string {
	value = boundedDiagnosticToken(value, maxRunes)
	if _, ok := allowed[value]; !ok {
		return ""
	}
	return value
}

func boundedDiagnosticTokens(value any, limit, maxRunes int, allowed map[string]struct{}) []string {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}
	default:
		return nil
	}
	result := make([]string, 0, min(len(values), limit))
	seen := make(map[string]struct{}, limit)
	for _, raw := range values {
		text := boundedDiagnosticToken(stringValue(raw), maxRunes)
		if text == "" {
			continue
		}
		if _, ok := allowed[text]; !ok {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
		if len(result) == limit {
			break
		}
	}
	return result
}

func boundedDiagnosticToken(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func boundedDiagnosticIdentifier(value string) string {
	return boundedDiagnosticToken(value, 128)
}

func boundedDiagnosticStatus(value any) int {
	status := boundedDiagnosticInt(value, 599)
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func boundedDiagnosticInt(value any, max int) int {
	var number int
	switch typed := value.(type) {
	case int:
		number = typed
	case int64:
		number = int(typed)
	case float64:
		number = int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			number = int(parsed)
		}
	}
	if number < 0 {
		return 0
	}
	if number > max {
		return max
	}
	return number
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func normalizeSubmissionDiagnosticServiceFilter(filter SubmissionDiagnosticFilter) (SubmissionDiagnosticFilter, error) {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	filter.ProcessingState = strings.TrimSpace(filter.ProcessingState)
	if filter.TeamID != "" {
		if _, err := uuid.Parse(filter.TeamID); err != nil {
			return SubmissionDiagnosticFilter{}, fmt.Errorf("team_id must be a UUID: %w", err)
		}
	}
	switch filter.ProcessingState {
	case "", "queued", "processing", "awaiting_review", "completed", "rejected", "quarantined", "failed":
	default:
		return SubmissionDiagnosticFilter{}, fmt.Errorf("processing_state is unsupported")
	}
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter, nil
}
