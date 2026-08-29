package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSynchronousRememberRejectedAttemptIsReplayableWithoutCanonicalState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-rejected-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-rejected-owner")
	repo := NewLedgerRepository(appDB, rls)
	attempt := synchronousRememberAttemptFixture(teamID, ownerID, "sync-rejected-key", "sync-rejected-hash")
	attempt.Outcome, attempt.FailedPhase, attempt.ErrorCode = "rejected", "commit", "stale_input"

	require.NoError(t, repo.RecordSynchronousRememberRejectedAttempt(ctx, attempt))
	require.ErrorIs(t, repo.RecordSynchronousRememberRejectedAttempt(ctx, attempt), ErrRememberReplay)
	loaded, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: attempt.IdempotencyKey})
	require.NoError(t, err)
	require.Equal(t, "rejected", loaded.Outcome)

	var canonicalRows int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_runs WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error
	}))
	assert.Zero(t, canonicalRows)

	var phase, eventKind, outcome, errorCode string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT e.phase, e.event_kind, a.outcome, a.error_code
			FROM remember_attempt_events AS e
			JOIN remember_attempts AS a ON a.team_id = e.team_id AND a.attempt_id = e.attempt_id
			WHERE e.team_id = ?::uuid AND e.owner_profile_id = ?::uuid
			ORDER BY e.sequence_no ASC
			LIMIT 1
		`, teamID, ownerID).Row().Scan(&phase, &eventKind, &outcome, &errorCode)
	}))
	assert.Equal(t, "commit", phase)
	assert.Equal(t, "commit_rejected", eventKind)
	assert.Equal(t, "rejected", outcome)
	assert.Equal(t, "stale_input", errorCode)
}
