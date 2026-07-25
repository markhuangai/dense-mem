package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2RelationshipConflictReviewerDismissesStaleCaseAfterRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "conflict-review-dismiss-stale", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "conflict-review-dismiss-stale-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-dismiss-stale-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-dismiss-stale-owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	active := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-dismiss-stale-a",
		"dismiss-stale-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-dismiss-stale-a",
	)
	stale := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-dismiss-stale-b",
		"dismiss-stale-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-dismiss-stale-b",
	)

	conflictID, conflictVersion := loadV2ConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	_, err := semanticRepo.RetractRelationship(ctx, V2RetractRelationshipInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		RelationshipID: stale.RelationshipResults[0].Relationship.RelationshipID,
		Reason:         "source corrected before conflict review",
		IdempotencyKey: "dismiss-stale-retract",
	})
	require.NoError(t, err)
	historicalKnownAt := time.Now().UTC()

	result := reviewV2ConflictCaseForTest(t, ctx, ledgerRepo, teamID, "conflict-reviewer-dismiss-stale", conflictID, time.Now().UTC())
	assert.Equal(t, V2ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, V2ConflictReviewStageDismissedNoConflict, result.Stage)

	var status string
	var version int
	var activeMembers, dismissedEvents int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&status, &version))
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND active
		`, teamID, conflictID).Scan(&activeMembers).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_events
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND action = 'dismissed'
		`, teamID, conflictID).Scan(&dismissedEvents).Error
	}))
	assert.Equal(t, "dismissed", status)
	assert.Greater(t, version, conflictVersion)
	assert.Equal(t, int64(1), activeMembers)
	assert.Equal(t, int64(1), dismissedEvents)

	var historicalConflicts []V2RelationshipConflictCaseRecord
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		var loadErr error
		historicalConflicts, loadErr = loadV2RelationshipConflictRecords(
			ctx,
			tx,
			teamID,
			[]string{active.RelationshipResults[0].Relationship.RelationshipID},
			&historicalKnownAt,
		)
		return loadErr
	}))
	require.Len(t, historicalConflicts, 1)
	assert.NotEqual(t, "dismissed", historicalConflicts[0].Status)
	assert.Empty(t, historicalConflicts[0].PreferredPositionID)
	assert.Nil(t, historicalConflicts[0].DismissedAt)
	require.NotEmpty(t, historicalConflicts[0].Positions)
	for _, position := range historicalConflicts[0].Positions {
		assert.Equal(t, "candidate", position.Disposition)
	}
}

func TestV2RelationshipConflictReviewerDoesNotExhaustAttemptsBeforeDueDate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "conflict-review-attempts", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "conflict-review-attempts-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-attempts-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-attempts-owner-b")
	ledgerRepo := NewV2LedgerRepositoryWithRuntimeConfig(appDB, rls, 20, V2ConflictRuntimeConfig{
		ReviewTTLDays: 2,
		Timezone:      "UTC",
	})
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-attempts-a",
		"attempts-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-attempts-a",
	)
	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-attempts-b",
		"attempts-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-attempts-b",
	)

	conflictID, _ := loadV2ConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	result := reviewV2ConflictCaseForTest(t, ctx, ledgerRepo, teamID, "conflict-reviewer-attempts", conflictID, time.Now().UTC())
	assert.Equal(t, V2ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, V2ConflictReviewStageWaitingForReviewDue, result.Stage)

	var status string
	var attempts int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, attempts
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&status, &attempts)
	}))
	assert.Equal(t, "open", status)
	assert.Equal(t, 0, attempts)
}

func reviewV2ConflictCaseForTest(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	workerID string,
	conflictID string,
	reviewNow time.Time,
) *V2ReviewRelationshipConflictCaseResult {
	t.Helper()
	run, claimed, err := repo.ReserveV2RelationshipConflictReviewRun(ctx, V2ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     workerID,
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := repo.ClaimV2RelationshipConflictCases(ctx, V2ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    workerID,
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	assert.Equal(t, conflictID, cases[0].ConflictID)
	result, err := repo.ReviewV2RelationshipConflictCase(ctx, V2ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    workerID,
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	return result
}
