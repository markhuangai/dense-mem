package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

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
	TeamID          string                               `json:"team_id"`
	TeamName        string                               `json:"team_name"`
	OwnerProfileID  string                               `json:"owner_profile_id"`
	SubmissionID    string                               `json:"submission_id"`
	ProcessingState string                               `json:"processing_state"`
	CorrelationID   string                               `json:"correlation_id,omitempty"`
	Attempts        int                                  `json:"attempts"`
	MaxAttempts     int                                  `json:"max_attempts"`
	EvidenceCount   int                                  `json:"evidence_count"`
	SubmittedAt     time.Time                            `json:"submitted_at"`
	NextAttemptAt   *time.Time                           `json:"next_attempt_at,omitempty"`
	StartedAt       *time.Time                           `json:"started_at,omitempty"`
	UpdatedAt       *time.Time                           `json:"updated_at,omitempty"`
	CompletedAt     *time.Time                           `json:"completed_at,omitempty"`
	Error           *memoryservice.SubmissionStatusError `json:"error,omitempty"`
}

type SubmissionDiagnosticPage struct {
	Items []SubmissionDiagnosticSummary
	Total int64
}

type SubmissionDiagnosticDetail struct {
	memoryservice.SubmissionStatusResult
	TeamID         string `json:"team_id"`
	TeamName       string `json:"team_name"`
	OwnerProfileID string `json:"owner_profile_id"`
	EvidenceCount  int    `json:"evidence_count"`
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
	return &SubmissionDiagnosticDetail{
		SubmissionStatusResult: *status,
		TeamID:                 record.Placement.TeamID,
		TeamName:               record.TeamName,
		OwnerProfileID:         record.Placement.OwnerProfileID,
		EvidenceCount:          record.EvidenceCount,
	}, nil
}

func submissionDiagnosticSummary(record repository.SubmissionDiagnosticRecord) SubmissionDiagnosticSummary {
	status := memoryservice.ProjectSubmissionStatus(&record.Placement)
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
		TeamID:          record.Placement.TeamID,
		TeamName:        record.TeamName,
		OwnerProfileID:  record.Placement.OwnerProfileID,
		SubmissionID:    record.Placement.IngestID,
		ProcessingState: status.ProcessingState,
		CorrelationID:   record.Placement.CorrelationID,
		Attempts:        record.Placement.Attempts,
		MaxAttempts:     record.Placement.MaxAttempts,
		EvidenceCount:   record.EvidenceCount,
		SubmittedAt:     submittedAt,
		NextAttemptAt:   record.Placement.NextAttemptAt,
		StartedAt:       record.Placement.StartedAt,
		UpdatedAt:       record.Placement.UpdatedAt,
		CompletedAt:     record.Placement.CompletedAt,
		Error:           statusError,
	}
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
