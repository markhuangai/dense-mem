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
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
)

var (
	ErrRememberAttemptDiagnosticNotFound     = errors.New("remember attempt diagnostic not found")
	ErrRememberAttemptDiagnosticsUnavailable = errors.New("remember attempt diagnostics unavailable")
	ErrRememberFailureArtifactNotFound       = errors.New("remember failure artifact not found")
)

type RememberAttemptDiagnosticsReader interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticDetail, error)
	GetRememberFailureArtifact(context.Context, string, string, string) (*RememberFailureArtifact, error)
}

type RememberAttemptDiagnosticFilter struct {
	TeamID  string
	Outcome string
	Limit   int
	Offset  int
}

type RememberAttemptDiagnosticPage struct {
	Items []RememberAttemptDiagnosticSummary
	Total int64
}

type RememberAttemptDiagnosticSummary struct {
	TeamID             string     `json:"team_id"`
	TeamName           string     `json:"team_name"`
	OwnerProfileID     string     `json:"owner_profile_id"`
	AttemptID          string     `json:"attempt_id"`
	SpaceID            string     `json:"space_id,omitempty"`
	SpaceGeneration    int64      `json:"space_generation,omitempty"`
	CanonicalAttemptID string     `json:"canonical_attempt_id,omitempty"`
	ContractVersion    string     `json:"contract_version"`
	SubmissionKind     string     `json:"submission_kind"`
	Outcome            string     `json:"outcome"`
	FailedPhase        string     `json:"failed_phase,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	Retryable          bool       `json:"retryable"`
	CorrelationID      string     `json:"correlation_id,omitempty"`
	EvidenceCount      int        `json:"evidence_count"`
	RelationshipCount  int        `json:"relationship_count"`
	DocumentCount      int        `json:"document_count"`
	AssessorTurns      int        `json:"assessor_turns"`
	DurationMS         int64      `json:"duration_ms"`
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type RememberAttemptDiagnosticDetail struct {
	RememberAttemptDiagnosticSummary
	PublicResult *RememberAttemptPublicResult        `json:"public_result"`
	Events       []RememberAttemptDiagnosticEvent    `json:"events"`
	Artifacts    []RememberFailureArtifactDescriptor `json:"artifacts"`
}

// RememberAttemptPublicResult is the existing terminal result schema. The
// service decodes only these allowlisted fields and never forwards the stored
// JSON object wholesale.
type RememberAttemptPublicResult = remember.TerminalRememberResult

type RememberAttemptDiagnosticEvent struct {
	SequenceNo int            `json:"sequence_no"`
	Phase      string         `json:"phase"`
	EventKind  string         `json:"event_kind"`
	Outcome    string         `json:"outcome"`
	Metadata   map[string]any `json:"metadata"`
	CreatedAt  time.Time      `json:"created_at"`
}

type RememberFailureArtifactDescriptor struct {
	ArtifactID          string    `json:"artifact_id"`
	ArtifactKind        string    `json:"artifact_kind"`
	ContentType         string    `json:"content_type"`
	ByteCount           int64     `json:"byte_count"`
	ContentSHA256       string    `json:"content_sha256"`
	CapturedAt          time.Time `json:"captured_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	RetainedByLegalHold bool      `json:"retained_by_legal_hold"`
}

type RememberFailureArtifact struct {
	ArtifactID          string
	ArtifactKind        string
	ContentType         string
	Content             []byte
	ByteCount           int64
	ContentSHA256       string
	CapturedAt          time.Time
	ExpiresAt           time.Time
	RetainedByLegalHold bool
}

type RememberAttemptDiagnosticsService struct {
	repo repository.RememberAttemptDiagnosticsRepository
}

func NewRememberAttemptDiagnosticsService(repo repository.RememberAttemptDiagnosticsRepository) *RememberAttemptDiagnosticsService {
	return &RememberAttemptDiagnosticsService{repo: repo}
}

func (s *RememberAttemptDiagnosticsService) ListRememberAttemptDiagnostics(
	ctx context.Context,
	filter RememberAttemptDiagnosticFilter,
) (*RememberAttemptDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	normalized, err := normalizeRememberAttemptDiagnosticServiceFilter(filter)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.ListRememberAttemptDiagnostics(ctx, repository.RememberAttemptDiagnosticFilter{
		TeamID:  normalized.TeamID,
		Outcome: normalized.Outcome,
		Limit:   normalized.Limit,
		Offset:  normalized.Offset,
	})
	if err != nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	page := &RememberAttemptDiagnosticPage{Items: []RememberAttemptDiagnosticSummary{}}
	if records == nil {
		return page, nil
	}
	page.Total = records.Total
	page.Items = make([]RememberAttemptDiagnosticSummary, 0, len(records.Records))
	for index := range records.Records {
		page.Items = append(page.Items, rememberAttemptDiagnosticSummary(records.Records[index]))
	}
	return page, nil
}

func (s *RememberAttemptDiagnosticsService) GetRememberAttemptDiagnostic(
	ctx context.Context,
	teamID string,
	attemptID string,
) (*RememberAttemptDiagnosticDetail, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	teamID, attemptID = strings.TrimSpace(teamID), strings.TrimSpace(attemptID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		return nil, fmt.Errorf("attempt_id must be a UUID: %w", err)
	}
	record, err := s.repo.GetRememberAttemptDiagnostic(ctx, teamID, attemptID)
	if errors.Is(err, repository.ErrRememberAttemptDiagnosticNotFound) {
		return nil, ErrRememberAttemptDiagnosticNotFound
	}
	if err != nil || record == nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	result, err := projectRememberAttemptPublicResult(record.PublicResult)
	if err != nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	events := make([]RememberAttemptDiagnosticEvent, 0, len(record.Events))
	for _, event := range record.Events {
		metadata := event.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		events = append(events, RememberAttemptDiagnosticEvent{
			SequenceNo: event.SequenceNo,
			Phase:      event.Phase,
			EventKind:  event.EventKind,
			Outcome:    event.Outcome,
			Metadata:   metadata,
			CreatedAt:  event.CreatedAt.UTC(),
		})
	}
	artifacts := make([]RememberFailureArtifactDescriptor, 0, len(record.Artifacts))
	for _, artifact := range record.Artifacts {
		artifacts = append(artifacts, RememberFailureArtifactDescriptor{
			ArtifactID:          artifact.ArtifactID,
			ArtifactKind:        artifact.ArtifactKind,
			ContentType:         artifact.ContentType,
			ByteCount:           artifact.ByteCount,
			ContentSHA256:       artifact.ContentSHA256,
			CapturedAt:          artifact.CapturedAt.UTC(),
			ExpiresAt:           artifact.ExpiresAt.UTC(),
			RetainedByLegalHold: artifact.RetainedByLegalHold,
		})
	}
	return &RememberAttemptDiagnosticDetail{
		RememberAttemptDiagnosticSummary: rememberAttemptDiagnosticSummary(*record),
		PublicResult:                     result,
		Events:                           events,
		Artifacts:                        artifacts,
	}, nil
}

func (s *RememberAttemptDiagnosticsService) GetRememberFailureArtifact(
	ctx context.Context,
	teamID string,
	attemptID string,
	artifactID string,
) (*RememberFailureArtifact, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	for label, value := range map[string]string{"team_id": teamID, "attempt_id": attemptID, "artifact_id": artifactID} {
		if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
			return nil, fmt.Errorf("%s must be a UUID: %w", label, err)
		}
	}
	artifact, err := s.repo.GetRememberFailureArtifact(ctx, strings.TrimSpace(teamID), strings.TrimSpace(attemptID), strings.TrimSpace(artifactID))
	if errors.Is(err, repository.ErrRememberFailureArtifactNotFound) {
		return nil, ErrRememberFailureArtifactNotFound
	}
	if err != nil || artifact == nil {
		return nil, ErrRememberAttemptDiagnosticsUnavailable
	}
	return &RememberFailureArtifact{
		ArtifactID:          artifact.ArtifactID,
		ArtifactKind:        artifact.ArtifactKind,
		ContentType:         artifact.ContentType,
		Content:             append([]byte(nil), artifact.Content...),
		ByteCount:           artifact.ByteCount,
		ContentSHA256:       artifact.ContentSHA256,
		CapturedAt:          artifact.CapturedAt.UTC(),
		ExpiresAt:           artifact.ExpiresAt.UTC(),
		RetainedByLegalHold: artifact.RetainedByLegalHold,
	}, nil
}

func rememberAttemptDiagnosticSummary(record repository.RememberAttemptDiagnosticRecord) RememberAttemptDiagnosticSummary {
	return RememberAttemptDiagnosticSummary{
		TeamID:             record.TeamID,
		TeamName:           record.TeamName,
		OwnerProfileID:     record.OwnerProfileID,
		AttemptID:          record.AttemptID,
		SpaceID:            record.SpaceID,
		SpaceGeneration:    record.SpaceGeneration,
		CanonicalAttemptID: record.CanonicalAttemptID,
		ContractVersion:    record.ContractVersion,
		SubmissionKind:     record.SubmissionKind,
		Outcome:            record.Outcome,
		FailedPhase:        record.FailedPhase,
		ErrorCode:          record.ErrorCode,
		Retryable:          record.Retryable,
		CorrelationID:      record.CorrelationID,
		EvidenceCount:      record.EvidenceCount,
		RelationshipCount:  record.RelationshipCount,
		DocumentCount:      record.DocumentCount,
		AssessorTurns:      record.AssessorTurns,
		DurationMS:         record.Duration.Milliseconds(),
		CreatedAt:          record.CreatedAt.UTC(),
		CompletedAt:        utcTimePtr(record.CompletedAt),
	}
}

func projectRememberAttemptPublicResult(raw map[string]any) (*RememberAttemptPublicResult, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var result RememberAttemptPublicResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	result.Kind = remember.ResultKindTerminal
	if result.Evidence == nil {
		result.Evidence = []remember.TerminalEvidenceResult{}
	}
	if result.RelationshipResults == nil {
		result.RelationshipResults = []remember.SubmissionRelationshipResult{}
	}
	if result.Errors == nil {
		result.Errors = []remember.SubmissionStatusError{}
	}
	return &result, nil
}

func normalizeRememberAttemptDiagnosticServiceFilter(filter RememberAttemptDiagnosticFilter) (RememberAttemptDiagnosticFilter, error) {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	filter.Outcome = strings.TrimSpace(filter.Outcome)
	if filter.TeamID != "" {
		if _, err := uuid.Parse(filter.TeamID); err != nil {
			return RememberAttemptDiagnosticFilter{}, fmt.Errorf("team_id must be a UUID: %w", err)
		}
	}
	switch filter.Outcome {
	case "", "completed", "rejected", "quarantined", "failed", "replayed":
	default:
		return RememberAttemptDiagnosticFilter{}, fmt.Errorf("outcome is unsupported")
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

func utcTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
