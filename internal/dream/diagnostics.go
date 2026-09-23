package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type DreamDiagnostic struct {
	CaptureID     string         `json:"capture_id"`
	TeamID        string         `json:"team_id"`
	RunID         string         `json:"run_id"`
	HypothesisID  string         `json:"hypothesis_id,omitempty"`
	Phase         string         `json:"phase"`
	Outcome       string         `json:"outcome"`
	Cause         string         `json:"cause,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	CaptureState  string         `json:"capture_state"`
	CaptureReason string         `json:"capture_reason,omitempty"`
	CapturedAt    *time.Time     `json:"captured_at,omitempty"`
	ExpiresAt     time.Time      `json:"expires_at"`
	CreatedAt     time.Time      `json:"created_at"`
}

type DreamDiagnosticPage struct {
	Items      []*DreamDiagnostic `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// Keep transport error checks at the application boundary while preserving
// the contract package's stable sentinel identity.
var (
	ErrDreamDiagnosticNotFound      = dreamcontract.ErrDreamDiagnosticNotFound
	ErrInvalidDreamDiagnosticCursor = dreamcontract.ErrInvalidDreamDiagnosticCursor
)

type DiagnosticService interface {
	List(ctx context.Context, teamID, runID string, limit int, cursor string) (*DreamDiagnosticPage, error)
	ListForHypothesis(ctx context.Context, teamID, hypothesisID string, limit int, cursor string) (*DreamDiagnosticPage, error)
	Get(ctx context.Context, teamID, runID, captureID string) (*DreamDiagnostic, error)
}

type diagnosticService struct {
	repo dreamcontract.DreamDiagnosticRepository
}

var _ DiagnosticService = (*diagnosticService)(nil)

func NewDiagnosticService(repo dreamcontract.DreamDiagnosticRepository) DiagnosticService {
	return &diagnosticService{repo: repo}
}

func (s *diagnosticService) List(ctx context.Context, teamID, runID string, limit int, cursor string) (*DreamDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("dream diagnostics: repository is required")
	}
	teamID, runID = strings.TrimSpace(teamID), strings.TrimSpace(runID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("run_id must be a UUID: %w", err)
	}
	page, err := s.repo.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{
		TeamID: teamID, RunID: runID, Limit: limit, Cursor: cursor,
	})
	if err != nil {
		return nil, err
	}
	result := &DreamDiagnosticPage{Items: make([]*DreamDiagnostic, 0, len(page.Items)), NextCursor: page.NextCursor}
	for index := range page.Items {
		result.Items = append(result.Items, projectDreamDiagnostic(&page.Items[index]))
	}
	return result, nil
}

func (s *diagnosticService) ListForHypothesis(ctx context.Context, teamID, hypothesisID string, limit int, cursor string) (*DreamDiagnosticPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("dream diagnostics: repository is required")
	}
	teamID, hypothesisID = strings.TrimSpace(teamID), strings.TrimSpace(hypothesisID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(hypothesisID); err != nil {
		return nil, fmt.Errorf("hypothesis_id must be a UUID: %w", err)
	}
	page, err := s.repo.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{
		TeamID: teamID, HypothesisID: hypothesisID, Limit: limit, Cursor: cursor,
	})
	if err != nil {
		return nil, err
	}
	result := &DreamDiagnosticPage{Items: make([]*DreamDiagnostic, 0, len(page.Items)), NextCursor: page.NextCursor}
	for index := range page.Items {
		result.Items = append(result.Items, projectDreamDiagnostic(&page.Items[index]))
	}
	return result, nil
}

func (s *diagnosticService) Get(ctx context.Context, teamID, runID, captureID string) (*DreamDiagnostic, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("dream diagnostics: repository is required")
	}
	teamID, runID, captureID = strings.TrimSpace(teamID), strings.TrimSpace(runID), strings.TrimSpace(captureID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("run_id must be a UUID: %w", err)
	}
	if _, err := uuid.Parse(captureID); err != nil {
		return nil, fmt.Errorf("capture_id must be a UUID: %w", err)
	}
	record, err := s.repo.GetDreamDiagnostic(ctx, teamID, runID, captureID)
	if errors.Is(err, dreamcontract.ErrDreamDiagnosticNotFound) {
		return nil, dreamcontract.ErrDreamDiagnosticNotFound
	}
	if err != nil {
		return nil, err
	}
	return projectDreamDiagnostic(record), nil
}

func projectDreamDiagnostic(record *dreamcontract.DreamDiagnosticCapture) *DreamDiagnostic {
	if record == nil {
		return nil
	}
	result := &DreamDiagnostic{
		CaptureID: record.CaptureID, TeamID: record.TeamID, RunID: record.RunID,
		HypothesisID: record.HypothesisID,
		Phase:        record.Phase, Outcome: record.Outcome, Cause: record.Cause,
		CaptureState: record.CaptureState, CaptureReason: record.CaptureReason,
		CapturedAt: record.CapturedAt, ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt,
	}
	if result.CaptureState == "expired" || result.CaptureReason == "retention_expired" {
		return result
	}
	if record.Details != nil {
		result.Details = make(map[string]any, len(record.Details))
		for key, value := range record.Details {
			result.Details[key] = value
		}
	}
	if len(record.Payload) > 0 && string(record.Payload) != "{}" {
		var payload map[string]any
		if json.Unmarshal(record.Payload, &payload) == nil {
			result.Payload = payload
		}
	}
	return result
}
