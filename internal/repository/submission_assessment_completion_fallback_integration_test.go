package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSubmissionAssessmentCompletionDerivesDefaultRelationshipResults(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-completion-fallback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-completion-fallback-owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		IdempotencyKey: "submission-completion-fallback", RequestHash: "submission-completion-fallback-hash",
		TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{
			{"ref": "r:first"},
			{"ref": "r:second"},
		}},
		Evidence: []EvidenceInput{
			{Content: "First staged relationship evidence."},
			{Content: "Second staged relationship evidence."},
		},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-completion-fallback-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = repo.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-completion-fallback-worker",
			ExpectedAttempts: claimed.Attempts, MaxAttempts: claimed.MaxAttempts,
		},
		Status:                          string(domain.SemanticReviewTerminalFailure),
		Category:                        "failed",
		DefaultRelationshipResultReason: "internal_failure",
	})
	require.NoError(t, err)
	status, err := repo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.RelationshipResults, 2)
	refs := map[string]bool{}
	for _, result := range status.RelationshipResults {
		refs[result.RelationshipRef] = true
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, "internal_failure", result.Reason)
		assert.Empty(t, result.Splits)
	}
	assert.Equal(t, map[string]bool{"r:first": true, "r:second": true}, refs)
}
