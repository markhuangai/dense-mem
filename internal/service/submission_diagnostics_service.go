package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

var (
	ErrSubmissionDiagnosticNotFound     = errors.New("remember attempt not found")
	ErrSubmissionDiagnosticsUnavailable = errors.New("remember attempts unavailable")
)

type SubmissionDiagnosticsReader interface {
	ListSubmissionDiagnostics(context.Context, SubmissionDiagnosticFilter) (*SubmissionDiagnosticPage, error)
	GetSubmissionDiagnostic(context.Context, string, string) (*SubmissionDiagnosticDetail, error)
}

type SubmissionDiagnosticFilter struct {
	TeamID          string
	ProcessingState string
	Limit           int
	Offset          int
}

type SubmissionDiagnosticSummary struct {
	TeamID            string     `json:"team_id"`
	TeamName          string     `json:"team_name"`
	OwnerProfileID    string     `json:"owner_profile_id"`
	SubmissionID      string     `json:"submission_id"`
	ProcessingState   string     `json:"processing_state"`
	CorrelationID     string     `json:"correlation_id,omitempty"`
	FailedPhase       string     `json:"failed_phase,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
	EvidenceCount     int        `json:"evidence_count"`
	RelationshipCount int        `json:"relationship_count"`
	DocumentCount     int        `json:"document_count"`
	AssessorTurns     int        `json:"assessor_turns"`
	DurationMS        int64      `json:"duration_ms"`
	CreatedAt         time.Time  `json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type SubmissionDiagnosticPage struct {
	Items []SubmissionDiagnosticSummary
	Total int64
}

type SubmissionDiagnosticDetail struct {
	memoryservice.SubmissionStatusResult
	TeamID            string                                         `json:"team_id"`
	TeamName          string                                         `json:"team_name"`
	OwnerProfileID    string                                         `json:"owner_profile_id"`
	FailedPhase       string                                         `json:"failed_phase,omitempty"`
	ErrorCode         string                                         `json:"error_code,omitempty"`
	EvidenceCount     int                                            `json:"evidence_count"`
	RelationshipCount int                                            `json:"relationship_count"`
	DocumentCount     int                                            `json:"document_count"`
	AssessorTurns     int                                            `json:"assessor_turns"`
	DurationMS        int64                                          `json:"duration_ms"`
	CreatedAt         time.Time                                      `json:"created_at"`
	CompletedAt       *time.Time                                     `json:"completed_at,omitempty"`
	Events            []repository.SubmissionDiagnosticEvent         `json:"events"`
	Artifacts         []repository.RememberFailureArtifactDescriptor `json:"failure_artifacts,omitempty"`
}

type SubmissionDiagnosticsService struct {
	repo repository.SubmissionDiagnosticsRepository
}

// GetRememberFailureArtifact is intentionally exposed only by the concrete
// control service. Request/profile transports do not receive this capability.
func (s *SubmissionDiagnosticsService) GetRememberFailureArtifact(ctx context.Context, teamID, attemptID, artifactID string) (*repository.RememberFailureArtifact, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	reader, ok := s.repo.(repository.RememberFailureArtifactReader)
	if !ok {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	artifact, err := reader.GetRememberFailureArtifact(ctx, strings.TrimSpace(teamID), strings.TrimSpace(attemptID), strings.TrimSpace(artifactID))
	if errors.Is(err, repository.ErrRememberFailureArtifactNotFound) {
		return nil, ErrSubmissionDiagnosticNotFound
	}
	if err != nil || artifact == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	if !artifact.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrSubmissionDiagnosticNotFound
	}
	return artifact, nil
}

func NewSubmissionDiagnosticsService(repo repository.SubmissionDiagnosticsRepository) *SubmissionDiagnosticsService {
	return &SubmissionDiagnosticsService{repo: repo}
}

func (s *SubmissionDiagnosticsService) ListSubmissionDiagnostics(ctx context.Context, filter SubmissionDiagnosticFilter) (*SubmissionDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSubmissionDiagnosticsUnavailable
	}
	normalized, err := normalizeRememberAttemptDiagnosticServiceFilter(filter)
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
	for _, record := range records.Records {
		page.Items = append(page.Items, rememberAttemptSummary(record))
	}
	return page, nil
}

func (s *SubmissionDiagnosticsService) GetSubmissionDiagnostic(ctx context.Context, teamID, submissionID string) (*SubmissionDiagnosticDetail, error) {
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
	status := rememberAttemptResult(record.PublicResult)
	return &SubmissionDiagnosticDetail{
		SubmissionStatusResult: *status,
		TeamID:                 record.TeamID, TeamName: record.TeamName, OwnerProfileID: record.OwnerProfileID,
		FailedPhase: record.FailedPhase, ErrorCode: record.ErrorCode,
		EvidenceCount: record.EvidenceCount, RelationshipCount: record.RelationshipCount,
		DocumentCount: record.DocumentCount, AssessorTurns: record.AssessorTurns,
		DurationMS: record.Duration.Milliseconds(), CreatedAt: record.CreatedAt,
		CompletedAt: record.CompletedAt, Events: record.Events,
		Artifacts: record.Artifacts,
	}, nil
}

func rememberAttemptSummary(record repository.SubmissionDiagnosticRecord) SubmissionDiagnosticSummary {
	return SubmissionDiagnosticSummary{
		TeamID: record.TeamID, TeamName: record.TeamName, OwnerProfileID: record.OwnerProfileID,
		SubmissionID: record.SubmissionID, ProcessingState: record.ProcessingState,
		CorrelationID: record.CorrelationID, FailedPhase: record.FailedPhase, ErrorCode: record.ErrorCode,
		EvidenceCount: record.EvidenceCount, RelationshipCount: record.RelationshipCount,
		DocumentCount: record.DocumentCount, AssessorTurns: record.AssessorTurns,
		DurationMS: record.Duration.Milliseconds(), CreatedAt: record.CreatedAt,
		CompletedAt: record.CompletedAt,
	}
}

func rememberAttemptResult(public map[string]any) *memoryservice.SubmissionStatusResult {
	result := &memoryservice.SubmissionStatusResult{
		ContractVersion:     "dense-mem.v2.6.1",
		SubmissionKind:      "remember",
		Evidence:            []memoryservice.SubmissionEvidenceStatus{},
		RelationshipResults: []memoryservice.SubmissionRelationshipResult{},
		Errors:              []memoryservice.SubmissionStatusError{},
	}
	encoded, err := json.Marshal(public)
	if err == nil {
		_ = json.Unmarshal(encoded, result)
	}
	if result.Evidence == nil {
		result.Evidence = []memoryservice.SubmissionEvidenceStatus{}
	}
	if result.RelationshipResults == nil {
		result.RelationshipResults = []memoryservice.SubmissionRelationshipResult{}
	}
	if result.Errors == nil {
		result.Errors = []memoryservice.SubmissionStatusError{}
	}
	return result
}

func normalizeRememberAttemptDiagnosticServiceFilter(filter SubmissionDiagnosticFilter) (SubmissionDiagnosticFilter, error) {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	if filter.TeamID != "" {
		parsed, err := uuid.Parse(filter.TeamID)
		if err != nil {
			return SubmissionDiagnosticFilter{}, fmt.Errorf("team_id must be a UUID: %w", err)
		}
		filter.TeamID = parsed.String()
	}
	filter.ProcessingState = strings.TrimSpace(filter.ProcessingState)
	switch filter.ProcessingState {
	case "", "completed", "rejected", "quarantined", "failed", "replayed":
	default:
		return SubmissionDiagnosticFilter{}, errors.New("processing_state is unsupported")
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
