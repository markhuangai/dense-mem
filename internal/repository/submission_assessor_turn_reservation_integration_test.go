package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionAssessmentRequeueCarriesReservedTurnsToNextClaim(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessor-turn-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessor-turn-owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessor-turn")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-assessor-turn-worker-a", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Zero(t, claimed.AssessorTurnsReserved)
	scope := SubmissionAssessmentRunScope{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-assessor-turn-worker-a",
		ExpectedAttempts: claimed.Attempts, MaxAttempts: claimed.MaxAttempts,
	}
	reserved, err := repo.ReserveSubmissionAssessorAttempt(ctx, ReserveSubmissionAssessorAttemptInput{
		SubmissionAssessmentRunScope: scope,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	_, err = repo.RequeueSubmissionAssessment(ctx, RequeueSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_attempt",
		ReleaseAssessorAttempt:       true,
		AssessorTurnsReserved:        2,
	})
	require.NoError(t, err)

	var storedTurns int
	var attemptReleased bool
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT assessor_turns_reserved, assessor_attempt_id IS NULL
			FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&storedTurns, &attemptReleased)
	}))
	assert.Equal(t, 2, storedTurns)
	assert.True(t, attemptReleased)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_runs SET available_at = now()
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error
	}))

	reclaimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-assessor-turn-worker-b", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, 2, reclaimed.AssessorTurnsReserved)
	assert.Equal(t, claimed.Attempts+1, reclaimed.Attempts)
}
