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
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberAttemptDiagnosticsServiceProjectsBoundedListAndDetail(t *testing.T) {
	teamID, attemptID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &rememberAttemptDiagnosticsRepoStub{
		page: &repository.RememberAttemptDiagnosticRecordPage{Total: 1, Records: []repository.RememberAttemptDiagnosticRecord{{
			TeamID: teamID, TeamName: "Team", OwnerProfileID: uuid.NewString(), AttemptID: attemptID,
			ContractVersion: "dense-mem.v2.6", SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "assessment", ErrorCode: "provider_unavailable", Duration: 2 * time.Millisecond,
			PublicResult: map[string]any{"submission_id": attemptID, "processing_state": "failed", "secret": "must-not-escape-list"},
		}}},
		detail: &repository.RememberAttemptDiagnosticRecord{
			TeamID: teamID, TeamName: "Team", OwnerProfileID: uuid.NewString(), AttemptID: attemptID,
			ContractVersion: "dense-mem.v2.6", SubmissionKind: "remember", Outcome: "failed",
			PublicResult: map[string]any{
				"contract_version": "dense-mem.v2.6", "submission_id": attemptID, "submission_kind": "remember",
				"processing_state": "failed", "search_state": "not_required", "correlation_id": "corr",
				"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
				"secret": "must-not-escape-detail",
			},
			Events:    []repository.RememberAttemptDiagnosticEvent{{SequenceNo: 1, Phase: "assessment", EventKind: "assessment_failed", Outcome: "failed", Metadata: map[string]any{"markup": "<b>text</b>"}}},
			Artifacts: []repository.RememberFailureArtifactDescriptor{{ArtifactID: artifactID, ArtifactKind: "failure", ContentType: "application/json", ByteCount: 47, ContentSHA256: "sha256:abc", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}},
		},
		artifact: &repository.RememberFailureArtifact{ArtifactID: artifactID, AttemptID: attemptID, ContentType: "application/json", Content: []byte(`{"phase":"assessment"}`), ByteCount: 23, ContentSHA256: "sha256:abc"},
	}
	svc := NewRememberAttemptDiagnosticsService(repo)

	page, err := svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{TeamID: teamID, Outcome: "failed"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, attemptID, page.Items[0].AttemptID)
	encoded := mustJSON(t, page.Items[0])
	require.NotContains(t, encoded, "must-not-escape-list")

	detail, err := svc.GetRememberAttemptDiagnostic(context.Background(), teamID, attemptID)
	require.NoError(t, err)
	require.NotNil(t, detail.PublicResult)
	require.Empty(t, detail.PublicResult.Evidence)
	require.Len(t, detail.Events, 1)
	require.Len(t, detail.Artifacts, 1)
	require.NotContains(t, mustJSON(t, detail.PublicResult), "must-not-escape-detail")

	gotArtifact, err := svc.GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.NoError(t, err)
	require.Equal(t, repo.artifact.Content, gotArtifact.Content)
	require.NotSame(t, &repo.artifact.Content[0], &gotArtifact.Content[0])
}

func TestRememberAttemptDiagnosticsServiceValidatesScopeAndMapsNotFound(t *testing.T) {
	repo := &rememberAttemptDiagnosticsRepoStub{
		listErr:     repository.ErrRememberAttemptDiagnosticNotFound,
		detailErr:   repository.ErrRememberAttemptDiagnosticNotFound,
		artifactErr: repository.ErrRememberFailureArtifactNotFound,
	}
	svc := NewRememberAttemptDiagnosticsService(repo)

	_, err := svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{TeamID: "not-a-uuid"})
	require.Error(t, err)
	repo.listErr = nil
	for _, outcome := range []string{"completed", "rejected", "quarantined", "failed", "replayed"} {
		_, err = svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{Outcome: outcome})
		require.NoError(t, err)
		require.Equal(t, outcome, repo.listFilter.Outcome)
	}
	_, err = svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{Outcome: "unknown"})
	require.Error(t, err)
	_, err = svc.GetRememberAttemptDiagnostic(context.Background(), "not-a-uuid", uuid.NewString())
	require.Error(t, err)
	_, err = svc.GetRememberAttemptDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticNotFound)
	_, err = svc.GetRememberFailureArtifact(context.Background(), uuid.NewString(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
}

func TestRememberAttemptDiagnosticsServiceDoesNotExposeRawResultFields(t *testing.T) {
	result, err := projectRememberAttemptPublicResult(map[string]any{
		"submission_id": uuid.NewString(), "correlation_id": "corr", "secret": map[string]any{"token": "redact"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, mustJSON(t, result), "redact")
	require.Equal(t, remember.ResultKindTerminal, result.Kind)
}

func TestRememberAttemptDiagnosticsServiceHandlesUnavailableAndMalformedRecords(t *testing.T) {
	var nilService *RememberAttemptDiagnosticsService
	_, err := nilService.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{})
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)
	_, err = nilService.GetRememberAttemptDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)
	_, err = nilService.GetRememberFailureArtifact(context.Background(), uuid.NewString(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)

	svc := NewRememberAttemptDiagnosticsService(nil)
	_, err = svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{})
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)

	teamID, attemptID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &rememberAttemptDiagnosticsRepoStub{listErr: errors.New("unavailable"), detailErr: errors.New("unavailable"), artifactErr: errors.New("unavailable")}
	svc = NewRememberAttemptDiagnosticsService(repo)
	page, err := svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{TeamID: teamID, Limit: 1000, Offset: -1})
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)
	require.Nil(t, page)

	repo.listErr = nil
	page, err = svc.ListRememberAttemptDiagnostics(context.Background(), RememberAttemptDiagnosticFilter{TeamID: teamID, Limit: 1000, Offset: -1})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, int64(0), page.Total)
	require.Equal(t, 100, repo.listFilter.Limit)
	require.Zero(t, repo.listFilter.Offset)

	_, err = svc.GetRememberAttemptDiagnostic(context.Background(), teamID, "bad")
	require.ErrorContains(t, err, "attempt_id")
	_, err = svc.GetRememberAttemptDiagnostic(context.Background(), teamID, attemptID)
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)

	repo.detailErr = nil
	repo.detail = &repository.RememberAttemptDiagnosticRecord{
		TeamID: teamID, AttemptID: attemptID, Outcome: "failed",
		PublicResult: map[string]any{"evidence": "not-an-array"},
	}
	_, err = svc.GetRememberAttemptDiagnostic(context.Background(), teamID, attemptID)
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)

	completedAt := time.Now().UTC()
	repo.detail.PublicResult = nil
	repo.detail.CompletedAt = &completedAt
	repo.detail.Events = []repository.RememberAttemptDiagnosticEvent{{Metadata: nil}}
	detail, err := svc.GetRememberAttemptDiagnostic(context.Background(), teamID, attemptID)
	require.NoError(t, err)
	require.NotNil(t, detail.CompletedAt)
	require.Equal(t, completedAt, *detail.CompletedAt)
	require.Empty(t, detail.Events[0].Metadata)

	_, err = svc.GetRememberFailureArtifact(context.Background(), teamID, attemptID, "bad")
	require.ErrorContains(t, err, "artifact_id")
	_, err = svc.GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)

	repo.artifactErr = nil
	repo.artifact = nil
	_, err = svc.GetRememberFailureArtifact(context.Background(), teamID, attemptID, artifactID)
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticsUnavailable)
}

func TestProjectRememberAttemptPublicResultRejectsMalformedStoredJSON(t *testing.T) {
	_, err := projectRememberAttemptPublicResult(map[string]any{"unsupported": make(chan int)})
	require.Error(t, err)
	_, err = projectRememberAttemptPublicResult(map[string]any{"evidence": "not-an-array"})
	require.Error(t, err)
	result, err := projectRememberAttemptPublicResult(nil)
	require.NoError(t, err)
	require.Empty(t, result.Evidence)
	require.Empty(t, result.RelationshipResults)
	require.Empty(t, result.Errors)
}

type rememberAttemptDiagnosticsRepoStub struct {
	page        *repository.RememberAttemptDiagnosticRecordPage
	listErr     error
	listFilter  repository.RememberAttemptDiagnosticFilter
	detail      *repository.RememberAttemptDiagnosticRecord
	detailErr   error
	artifact    *repository.RememberFailureArtifact
	artifactErr error
}

func (s *rememberAttemptDiagnosticsRepoStub) ListRememberAttemptDiagnostics(_ context.Context, filter repository.RememberAttemptDiagnosticFilter) (*repository.RememberAttemptDiagnosticRecordPage, error) {
	s.listFilter = filter
	return s.page, s.listErr
}
func (s *rememberAttemptDiagnosticsRepoStub) GetRememberAttemptDiagnostic(context.Context, string, string) (*repository.RememberAttemptDiagnosticRecord, error) {
	return s.detail, s.detailErr
}
func (s *rememberAttemptDiagnosticsRepoStub) GetRememberFailureArtifact(context.Context, string, string, string) (*repository.RememberFailureArtifact, error) {
	return s.artifact, s.artifactErr
}
func (s *rememberAttemptDiagnosticsRepoStub) PurgeExpiredRememberFailureArtifacts(context.Context, int) (int, error) {
	return 0, errors.New("not used")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}
