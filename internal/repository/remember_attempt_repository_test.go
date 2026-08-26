package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRecordRememberFailureRejectsArtifactRetentionBeyondSevenDays(t *testing.T) {
	captured := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	err := (&LedgerRepositoryImpl{}).RecordRememberFailure(context.Background(), RememberFailureRecordInput{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), AttemptID: uuid.NewString(),
		IdempotencyKey: "retention-boundary", RequestHash: "request-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactKind: "failure", ContentType: "application/json", Content: []byte("{}"),
			CapturedAt: captured, ExpiresAt: captured.Add(7*24*time.Hour + time.Second),
		}},
	})
	require.ErrorContains(t, err, "retention exceeds seven days")
}

func TestRememberAttemptEventDraftsDescribeSafeTerminalPhases(t *testing.T) {
	events := rememberAttemptEventDrafts(RememberAttemptRecordInput{
		ContractVersion: "dense-mem.v2.6.1", Outcome: "completed", AssessorTurns: 2, DocumentCount: 3,
	})
	require.Len(t, events, 4)
	require.Equal(t, "request_validated", events[0].EventKind)
	require.Equal(t, "assessment_validated", events[1].EventKind)
	require.Equal(t, "embedding_batch_completed", events[2].EventKind)
	require.Equal(t, "commit_completed", events[3].EventKind)
	require.Equal(t, 2, events[2].Metadata["assessor_turns"])
	require.Equal(t, 3, events[2].Metadata["document_count"])
}
