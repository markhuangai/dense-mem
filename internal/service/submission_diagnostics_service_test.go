package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionDiagnosticsProjectsBoundedActionableState(t *testing.T) {
	now := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	teamID, ownerID, submissionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &submissionDiagnosticsRepoStub{page: &repository.SubmissionDiagnosticRecordPage{
		Records: []repository.SubmissionDiagnosticRecord{{
			TeamName:      "Staging",
			EvidenceCount: 1,
			Placement: repository.CreateIngestResult{
				TeamID: teamID, OwnerProfileID: ownerID, IngestID: submissionID,
				Status: string(domain.PlacementRunFailed), CorrelationID: "corr-diagnostics",
				Attempts: 5, MaxAttempts: 5, SubmittedAt: &now, UpdatedAt: &now, CompletedAt: &now,
				Items: []repository.PlacementItem{{Status: "failed", Result: map[string]any{
					"failure_stage": "assessment", "failure_class": "timeout",
				}}},
			},
		}},
		Total: 1,
	}}
	svc := NewSubmissionDiagnosticsService(repo)

	page, err := svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{
		TeamID: teamID, ProcessingState: "failed", Limit: 25,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	item := page.Items[0]
	require.Equal(t, "failed", item.ProcessingState)
	require.Equal(t, "corr-diagnostics", item.CorrelationID)
	require.Equal(t, 5, item.Attempts)
	require.NotNil(t, item.Error)
	require.Equal(t, "assessor_unavailable", item.Error.Code)
	require.True(t, item.Error.Retryable)
	require.Equal(t, "resubmit_submission", item.Error.NextAction)
	require.NotContains(t, item.Error.Message, "timeout")
	require.Equal(t, repository.SubmissionDiagnosticFilter{
		TeamID: teamID, ProcessingState: "failed", Limit: 25,
	}, repo.listFilter)
}

func TestSubmissionDiagnosticsDetailNeverReturnsEvidenceContentOrRawFailure(t *testing.T) {
	teamID, ownerID, submissionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	evidenceID := uuid.NewString()
	repo := &submissionDiagnosticsRepoStub{detail: &repository.SubmissionDiagnosticRecord{
		TeamName:      "Staging",
		EvidenceCount: 1,
		Placement: repository.CreateIngestResult{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: submissionID,
			Status:   string(domain.PlacementRunFailed),
			Evidence: []repository.EvidenceFragment{{FragmentID: evidenceID, EvidenceIndex: 0, Content: "must not cross diagnostics boundary"}},
			Items: []repository.PlacementItem{{FragmentID: evidenceID, Status: "failed", Result: map[string]any{
				"failure_stage": "unknown-provider-detail", "failure_class": "secret-provider-response",
			}}},
		},
	}}
	svc := NewSubmissionDiagnosticsService(repo)

	detail, err := svc.GetSubmissionDiagnostic(context.Background(), teamID, submissionID)
	require.NoError(t, err)
	require.Equal(t, teamID, detail.TeamID)
	require.Equal(t, ownerID, detail.OwnerProfileID)
	require.Len(t, detail.Evidence, 1)
	require.Equal(t, repo.detail.Placement.Evidence[0].FragmentID, detail.Evidence[0].EvidenceID)
	require.Len(t, detail.Errors, 1)
	require.Equal(t, "submission_processing_failed", detail.Errors[0].Code)
	require.NotContains(t, detail.Errors[0].Message, "secret-provider-response")
	require.NotContains(t, detail.Errors[0].Message, "unknown-provider-detail")
}

func TestSubmissionDiagnosticsValidatesScopeAndBoundsRepositoryErrors(t *testing.T) {
	svc := NewSubmissionDiagnosticsService(&submissionDiagnosticsRepoStub{})
	_, err := svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{TeamID: "bad"})
	require.ErrorContains(t, err, "team_id")
	_, err = svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{ProcessingState: "unknown"})
	require.ErrorContains(t, err, "processing_state")
	_, err = svc.GetSubmissionDiagnostic(context.Background(), "bad", uuid.NewString())
	require.ErrorContains(t, err, "team_id")

	repo := &submissionDiagnosticsRepoStub{err: errors.New("database detail")}
	svc = NewSubmissionDiagnosticsService(repo)
	_, err = svc.GetSubmissionDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	require.NotContains(t, err.Error(), "database detail")

	repo.err = repository.ErrSubmissionDiagnosticNotFound
	_, err = svc.GetSubmissionDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
}

type submissionDiagnosticsRepoStub struct {
	page       *repository.SubmissionDiagnosticRecordPage
	detail     *repository.SubmissionDiagnosticRecord
	listFilter repository.SubmissionDiagnosticFilter
	err        error
}

func (s *submissionDiagnosticsRepoStub) ListSubmissionDiagnostics(_ context.Context, filter repository.SubmissionDiagnosticFilter) (*repository.SubmissionDiagnosticRecordPage, error) {
	s.listFilter = filter
	return s.page, s.err
}

func (s *submissionDiagnosticsRepoStub) GetSubmissionDiagnostic(context.Context, string, string) (*repository.SubmissionDiagnosticRecord, error) {
	return s.detail, s.err
}
