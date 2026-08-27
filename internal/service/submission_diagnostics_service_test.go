package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type submissionDiagnosticsServiceRepoStub struct {
	page         *repository.SubmissionDiagnosticRecordPage
	detail       *repository.SubmissionDiagnosticRecord
	listErr      error
	detailErr    error
	artifact     *repository.RememberFailureArtifact
	artifactErr  error
	listFilter   repository.SubmissionDiagnosticFilter
	artifactArgs []string
}

type submissionDiagnosticsNoArtifactRepo struct{}

func (submissionDiagnosticsNoArtifactRepo) ListSubmissionDiagnostics(context.Context, repository.SubmissionDiagnosticFilter) (*repository.SubmissionDiagnosticRecordPage, error) {
	return nil, nil
}

func (submissionDiagnosticsNoArtifactRepo) GetSubmissionDiagnostic(context.Context, string, string) (*repository.SubmissionDiagnosticRecord, error) {
	return nil, nil
}

func (s *submissionDiagnosticsServiceRepoStub) ListSubmissionDiagnostics(_ context.Context, filter repository.SubmissionDiagnosticFilter) (*repository.SubmissionDiagnosticRecordPage, error) {
	s.listFilter = filter
	return s.page, s.listErr
}

func (s *submissionDiagnosticsServiceRepoStub) GetSubmissionDiagnostic(context.Context, string, string) (*repository.SubmissionDiagnosticRecord, error) {
	return s.detail, s.detailErr
}

func (s *submissionDiagnosticsServiceRepoStub) GetRememberFailureArtifact(_ context.Context, teamID, attemptID, artifactID string) (*repository.RememberFailureArtifact, error) {
	s.artifactArgs = []string{teamID, attemptID, artifactID}
	return s.artifact, s.artifactErr
}

func (s *submissionDiagnosticsServiceRepoStub) PurgeExpiredRememberFailureArtifacts(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func TestSubmissionDiagnosticsServiceListsAndNormalizesAttempts(t *testing.T) {
	teamID := uuid.NewString()
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	repo := &submissionDiagnosticsServiceRepoStub{page: &repository.SubmissionDiagnosticRecordPage{
		Total: 1,
		Records: []repository.SubmissionDiagnosticRecord{{
			TeamID: teamID, TeamName: "team", OwnerProfileID: uuid.NewString(), SubmissionID: uuid.NewString(),
			ProcessingState: "completed", CorrelationID: "corr", EvidenceCount: 2, RelationshipCount: 3,
			Historical: true, DocumentCount: 4, AssessorTurns: 2, Duration: 1500 * time.Millisecond, CreatedAt: now, CompletedAt: &now,
		}},
	}}
	page, err := NewSubmissionDiagnosticsService(repo).ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{
		TeamID: "  " + teamID + "  ", ProcessingState: " completed ", Limit: 1000, Offset: -4,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, teamID, page.Items[0].TeamID)
	require.True(t, page.Items[0].Historical)
	require.Equal(t, int64(1500), page.Items[0].DurationMS)
	require.Equal(t, repository.SubmissionDiagnosticFilter{TeamID: teamID, ProcessingState: "completed", Limit: 100, Offset: 0}, repo.listFilter)
}

func TestSubmissionDiagnosticsServiceListHandlesNilAndErrors(t *testing.T) {
	repo := &submissionDiagnosticsServiceRepoStub{}
	page, err := NewSubmissionDiagnosticsService(repo).ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.NoError(t, err)
	require.Empty(t, page.Items)

	repo.listErr = errors.New("database details must stay private")
	page, err = NewSubmissionDiagnosticsService(repo).ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	require.Nil(t, page)

	for _, filter := range []SubmissionDiagnosticFilter{
		{TeamID: "not-a-uuid"}, {ProcessingState: "processing"},
	} {
		_, err = NewSubmissionDiagnosticsService(&submissionDiagnosticsServiceRepoStub{}).ListSubmissionDiagnostics(context.Background(), filter)
		require.Error(t, err)
	}
	_, err = (*SubmissionDiagnosticsService)(nil).ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
}

func TestSubmissionDiagnosticsServiceGetsDetailAndMapsPublicResult(t *testing.T) {
	teamID, attemptID := uuid.NewString(), uuid.NewString()
	repo := &submissionDiagnosticsServiceRepoStub{detail: &repository.SubmissionDiagnosticRecord{
		TeamID: teamID, TeamName: "team", OwnerProfileID: uuid.NewString(), SubmissionID: attemptID,
		ProcessingState: "failed", FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
		EvidenceCount: 1, RelationshipCount: 2, DocumentCount: 3, AssessorTurns: 1,
		Historical: true, Duration: 2 * time.Second, PublicResult: map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": attemptID,
			"submission_kind": "remember", "processing_state": "failed", "search_state": "not_required",
			"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		}, Events: []repository.SubmissionDiagnosticEvent{{
			SequenceNo: 1, Phase: "legacy_cutover", EventKind: "legacy_terminalized", Outcome: "failed", Metadata: map[string]any{}, CreatedAt: time.Now().UTC(),
		}},
	}}
	detail, err := NewSubmissionDiagnosticsService(repo).GetSubmissionDiagnostic(context.Background(), "  "+teamID, attemptID)
	require.NoError(t, err)
	require.Equal(t, "team", detail.TeamName)
	require.Equal(t, "embedding", detail.FailedPhase)
	require.True(t, detail.Historical)
	require.Equal(t, int64(2000), detail.DurationMS)
	require.Equal(t, "dense-mem.v2.6.1", detail.ContractVersion)
	require.Empty(t, detail.Errors)
	require.Len(t, detail.Events, 1)
	require.Equal(t, "legacy_terminalized", detail.Events[0].EventKind)

	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	events := payload["events"].([]any)
	event := events[0].(map[string]any)
	require.Equal(t, "legacy_terminalized", event["event_kind"])
	require.Contains(t, event, "sequence_no")
	require.NotContains(t, event, "EventKind")

	for _, ids := range [][2]string{{"bad", attemptID}, {teamID, "bad"}} {
		_, err = NewSubmissionDiagnosticsService(repo).GetSubmissionDiagnostic(context.Background(), ids[0], ids[1])
		require.Error(t, err)
	}
	repo.detailErr = repository.ErrSubmissionDiagnosticNotFound
	_, err = NewSubmissionDiagnosticsService(repo).GetSubmissionDiagnostic(context.Background(), teamID, attemptID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	repo.detailErr = errors.New("database details")
	_, err = NewSubmissionDiagnosticsService(repo).GetSubmissionDiagnostic(context.Background(), teamID, attemptID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	_, err = (*SubmissionDiagnosticsService)(nil).GetSubmissionDiagnostic(context.Background(), teamID, attemptID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
}

func TestSubmissionDiagnosticsServiceReadsOnlyLiveArtifacts(t *testing.T) {
	teamID, attemptID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	repo := &submissionDiagnosticsServiceRepoStub{artifact: &repository.RememberFailureArtifact{
		TeamID: teamID, AttemptID: attemptID, ArtifactID: artifactID, ContentType: "application/json",
		Content: []byte(`{"safe":true}`), ExpiresAt: now.Add(time.Hour),
	}}
	artifact, err := NewSubmissionDiagnosticsService(repo).GetRememberFailureArtifact(context.Background(), " "+teamID, attemptID, artifactID)
	require.NoError(t, err)
	require.Equal(t, []string{teamID, attemptID, artifactID}, repo.artifactArgs)
	require.Equal(t, []byte(`{"safe":true}`), artifact.Content)

	repo.artifactErr = repository.ErrRememberFailureArtifactNotFound
	_, err = NewSubmissionDiagnosticsService(repo).GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	repo.artifactErr = errors.New("database details")
	_, err = NewSubmissionDiagnosticsService(repo).GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	repo.artifactErr = nil
	repo.artifact.ExpiresAt = now.Add(-time.Second)
	_, err = NewSubmissionDiagnosticsService(repo).GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)

	_, err = NewSubmissionDiagnosticsService(submissionDiagnosticsNoArtifactRepo{}).GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	_, err = (*SubmissionDiagnosticsService)(nil).GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
}
