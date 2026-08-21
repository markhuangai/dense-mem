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
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestSubmissionDiagnosticsProjectsBoundedActionableState(t *testing.T) {
	now := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	teamID, ownerID, submissionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &submissionDiagnosticsRepoStub{page: &repository.SubmissionDiagnosticRecordPage{
		Records: []repository.SubmissionDiagnosticRecord{{
			TeamName: "Staging",
			SourceTypes: []string{
				"document",
				`{"password":"supersecret"}`,
				"https://operator:secret@example.test/notes?token=opaque",
			},
			OperatorDiagnostic: map[string]any{
				"failure_reason_code": "assessor_provider_failed",
				"failure_stage":       "assessment",
				"failure_class":       "timeout",
				"failure_measurement": map[string]any{"unit": "tokens", "observed": 99, "limit": 10},
			},
			EvidenceCount: 1,
			Placement: repository.CreateIngestResult{
				TeamID: teamID, OwnerProfileID: ownerID, IngestID: submissionID,
				Status: string(domain.PlacementRunFailed), CorrelationID: "corr-diagnostics",
				Attempts: 5, MaxAttempts: 5, SubmittedAt: &now, UpdatedAt: &now, CompletedAt: &now,
				Items: []repository.PlacementItem{{Status: "failed", Result: map[string]any{
					"failure_code": string(memoryservice.SubmissionErrorRequiresResubmission),
					"resubmission_issues": []map[string]any{{
						"code": "entity_resolution_ambiguous", "relationship_ref": "rel-1",
						"component": "subject", "message": "choose one active Entity",
					}},
					"failure_reason_code": "assessor_provider_failed",
					"failure_stage":       "assessment",
					"failure_class":       "timeout",
					"failure_measurement": map[string]any{"unit": "tokens", "observed": 99, "limit": 10},
					"provider_response":   "must not cross diagnostics boundary",
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
	require.Equal(t, "submission_requires_resubmission", item.Error.Code)
	require.True(t, item.Error.Retryable)
	require.Equal(t, "resubmit_submission", item.Error.NextAction)
	require.Len(t, item.Error.ResubmissionIssues, 1)
	require.Equal(t, "rel-1", item.Error.ResubmissionIssues[0].RelationshipRef)
	require.NotContains(t, item.Error.Message, "timeout")
	require.NotContains(t, item.Error.Message, "must not cross")
	require.Equal(t, "document evidence", item.SourceSummary)
	require.False(t, item.SourceSummaryTruncated)
	require.NotContains(t, item.SourceSummary, "supersecret")
	require.NotContains(t, item.SourceSummary, "opaque")
	require.NotNil(t, item.OperatorDiagnostic)
	require.Equal(t, "assessor_provider_failed", item.OperatorDiagnostic.FailureReasonCode)
	require.Equal(t, "tokens", item.OperatorDiagnostic.FailureMeasurement.Unit)
	require.NotEmpty(t, item.OperatorDiagnostic.Message, "the list projection should synthesize a bounded message")
	require.NotContains(t, item.OperatorDiagnostic.Message, "must not cross")
	require.Equal(t, repository.SubmissionDiagnosticFilter{
		TeamID: teamID, ProcessingState: "failed", Limit: 25,
	}, repo.listFilter)
}

func TestSubmissionDiagnosticsDetailNeverReturnsEvidenceContentOrRawFailure(t *testing.T) {
	teamID, ownerID, submissionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	evidenceID := uuid.NewString()
	repo := &submissionDiagnosticsRepoStub{detail: &repository.SubmissionDiagnosticRecord{
		TeamName:      "Staging",
		SourceTypes:   []string{"observation", `{"access_token":"opaque-secret"}`},
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
	require.Equal(t, "observation evidence", detail.SourceSummary)
	require.NotContains(t, detail.SourceSummary, "opaque-secret")
	require.Len(t, detail.Evidence, 1)
	require.Equal(t, repo.detail.Placement.Evidence[0].FragmentID, detail.Evidence[0].EvidenceID)
	require.Len(t, detail.Errors, 1)
	require.Equal(t, "submission_processing_failed", detail.Errors[0].Code)
	require.NotContains(t, detail.Errors[0].Message, "secret-provider-response")
	require.NotContains(t, detail.Errors[0].Message, "unknown-provider-detail")
	require.Nil(t, detail.OperatorDiagnostic, "unknown diagnostic tokens must not cross the control boundary")
}

func TestSubmissionDiagnosticsDetailOrdersAndFiltersOperatorHistory(t *testing.T) {
	teamID, ownerID, submissionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	first := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	repo := &submissionDiagnosticsRepoStub{detail: &repository.SubmissionDiagnosticRecord{
		TeamName: "Staging",
		Placement: repository.CreateIngestResult{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: submissionID,
			Status: string(domain.PlacementRunFailed),
		},
		OperatorDiagnostics: []repository.SubmissionDiagnosticOperatorDiagnostic{
			{ID: "first", OutcomeKind: "semantic_assessment_attempt", Status: "queued", CreatedAt: first, Payload: map[string]any{
				"failure_reason_code": "assessor_provider_failed", "failure_stage": "assessment", "failure_class": "timeout",
			}},
			{ID: "second", OutcomeKind: "submission_assessment_terminal", Status: "failed", CreatedAt: second, Payload: map[string]any{
				"failure_reason_code": "assessor_response_invalid", "failure_stage": "assessment", "failure_class": "validation_failed",
			}},
			{ID: "secret", OutcomeKind: "internal", Status: "failed", CreatedAt: second.Add(time.Minute), Payload: map[string]any{
				"failure_stage": "provider-secret-detail", "failure_class": "provider-secret-detail",
			}},
		},
	}}
	detail, err := NewSubmissionDiagnosticsService(repo).GetSubmissionDiagnostic(context.Background(), teamID, submissionID)
	require.NoError(t, err)
	require.Len(t, detail.OperatorDiagnostics, 2)
	require.Equal(t, "first", detail.OperatorDiagnostics[0].ID)
	require.Equal(t, first, detail.OperatorDiagnostics[0].OccurredAt.UTC())
	require.Equal(t, "second", detail.OperatorDiagnostics[1].ID)
	require.Equal(t, second, detail.OperatorDiagnostics[1].OccurredAt.UTC())
	for _, diagnostic := range detail.OperatorDiagnostics {
		require.NotContains(t, diagnostic.Message, "provider-secret-detail")
	}
}

func TestSubmissionDiagnosticsValidatesScopeAndBoundsRepositoryErrors(t *testing.T) {
	svc := NewSubmissionDiagnosticsService(&submissionDiagnosticsRepoStub{})
	_, err := svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{TeamID: "bad"})
	require.ErrorContains(t, err, "team_id")
	_, err = svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{ProcessingState: "unknown"})
	require.ErrorContains(t, err, "processing_state")
	_, err = svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{ProcessingState: "rejected"})
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
