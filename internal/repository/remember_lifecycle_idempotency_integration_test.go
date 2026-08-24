package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRememberSupersessionNamespacesLifecycleIdempotencyKey(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-lifecycle-key-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-lifecycle-key-owner")
	ledger := NewLedgerRepository(appDB, rls)
	target := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-lifecycle-key-target", "Evidence to supersede.")
	retractTarget := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-lifecycle-key-retract-target", "Unrelated evidence to retract.")

	_, err := ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceIDs:    []string{retractTarget.Evidence[0].FragmentID},
		Reason:         "reserve the shared lifecycle key",
		IdempotencyKey: "remember-lifecycle-key",
		RequestHash:    "retract-request-hash",
	})
	require.NoError(t, err)

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-lifecycle-key",
		RequestHash:       "remember-request-hash",
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content:               "Replacement evidence.",
			SourceType:            "document",
			SupersedesEvidenceIDs: []string{target.Evidence[0].FragmentID},
		}},
	})
	require.NoError(t, err)

	scope := SubmissionAssessmentRunScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: staged.IngestID}
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return applyRememberSubmissionIntents(ctx, tx, scope)
	}))
	// A retry of the same accepted submission must replay the namespaced
	// operation rather than attempt a second terminal lifecycle event.
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return applyRememberSubmissionIntents(ctx, tx, scope)
	}))

	var lifecycleKey, action string
	var eventCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT idempotency_key, action
			FROM evidence_lifecycle_operations
			WHERE team_id = ?::uuid AND replacement_ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&lifecycleKey, &action); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND replacement_fragment_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Row().Scan(&eventCount)
	}))
	assert.Equal(t, rememberSupersessionLifecycleKey(staged.IngestID), lifecycleKey)
	assert.Equal(t, "supersede", action)
	assert.Equal(t, int64(1), eventCount)
}
