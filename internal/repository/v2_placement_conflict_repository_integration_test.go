package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2PlacementSemanticCommitOpensCrossProfileConflictCase(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-conflict", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-conflict-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-conflict-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-conflict-owner-b")
	ledgerRepo := NewV2LedgerRepositoryWithRuntimeConfig(appDB, rls, 20, V2ConflictRuntimeConfig{
		ReviewTTLDays: 2,
		Timezone:      "UTC",
	})
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	first := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-conflict-a",
		"conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-a",
	)
	second := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-conflict-b",
		"conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-b",
	)

	var conflictID, status, timezone string
	var reviewTTLDays int
	var positionCount, memberCount, eventCount int64
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT conflict_id::text, status, review_ttl_days, timezone
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&conflictID, &status, &reviewTTLDays, &timezone))
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&positionCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND fragment_id IS NOT NULL
		`, teamID, conflictID).Scan(&memberCount).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_events
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&eventCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "open", status)
	assert.Equal(t, 2, reviewTTLDays)
	assert.Equal(t, "UTC", timezone)
	assert.Equal(t, int64(2), positionCount)
	assert.Equal(t, int64(2), memberCount)
	assert.GreaterOrEqual(t, eventCount, int64(5))

	var conflicts []V2RelationshipConflictCaseRecord
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		var loadErr error
		conflicts, loadErr = loadV2RelationshipConflictRecords(ctx, tx, teamID, []string{
			first.RelationshipResults[0].Relationship.RelationshipID,
			second.RelationshipResults[0].Relationship.RelationshipID,
		})
		return loadErr
	})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	require.Len(t, conflicts[0].Positions, 2)
	assert.Equal(t, "open", conflicts[0].Status)
	assert.ElementsMatch(t, []string{
		first.RelationshipResults[0].Relationship.RelationshipID,
		second.RelationshipResults[0].Relationship.RelationshipID,
	}, append(conflicts[0].Positions[0].RelationshipIDs, conflicts[0].Positions[1].RelationshipIDs...))

	searchRepo := NewV2SearchRepository(appDB, rls)
	recall, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense-Mem",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, recall.Conflicts, 1)
	assert.Equal(t, conflictID, recall.Conflicts[0].ConflictID)
	assert.Len(t, recall.Conflicts[0].Positions, 2)
}

func TestV2PlacementSemanticCommitValidatesConflictContextAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-conflict-context", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-conflict-context-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-b")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-c")
	otherTeamID := createV2LedgerTeam(t, adminDB, rls, "placement-conflict-context-other-team")
	otherOwnerID := createV2LedgerProfile(t, adminDB, rls, otherTeamID, "placement-conflict-context-other-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	otherSubject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Other System")
	redis := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Redis")
	memcached := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Memcached")
	foreignSubject := createV2SemanticEntity(t, ctx, semanticRepo, otherTeamID, otherOwnerID, "project", "Foreign System")
	foreignObject := createV2SemanticEntity(t, ctx, semanticRepo, otherTeamID, otherOwnerID, "product", "SQLite")

	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-context-a",
		"context-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-context-a",
	)
	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-context-b",
		"context-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-context-b",
	)
	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-unrelated-a",
		"context-unrelated-a", "Other System uses Redis.", otherSubject.EntityID, redis.EntityID, "source-group-unrelated-a",
	)
	commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-unrelated-b",
		"context-unrelated-b", "Other System uses Memcached.", otherSubject.EntityID, memcached.EntityID, "source-group-unrelated-b",
	)

	conflictID, conflictVersion := loadV2ConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	unrelatedConflictID, unrelatedConflictVersion := loadV2ConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, otherSubject.EntityID)
	valid, err := attemptV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-context-c",
		"context-conflict-c", "Dense-Mem uses PostgreSQL according to owner C.", subject.EntityID, postgres.EntityID, "source-group-context-c",
		&V2PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
	)
	require.NoError(t, err)
	require.Len(t, valid.RelationshipResults, 1)
	assert.NotNil(t, valid.RelationshipResults[0].Relationship)

	var memberCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&memberCount).Error
	}))
	assert.Equal(t, int64(3), memberCount)

	for _, tc := range []struct {
		name            string
		teamID          string
		ownerID         string
		subjectEntityID string
		objectEntityID  string
		context         V2PlacementConflictContextInput
		beforeProfileID string
	}{
		{
			name:            "stale version",
			teamID:          teamID,
			ownerID:         ownerC,
			subjectEntityID: subject.EntityID,
			objectEntityID:  postgres.EntityID,
			context:         V2PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion + 1},
			beforeProfileID: ownerA,
		},
		{
			name:            "unrelated same-team case",
			teamID:          teamID,
			ownerID:         ownerC,
			subjectEntityID: subject.EntityID,
			objectEntityID:  postgres.EntityID,
			context:         V2PlacementConflictContextInput{ConflictID: unrelatedConflictID, ExpectedVersion: unrelatedConflictVersion},
			beforeProfileID: ownerA,
		},
		{
			name:            "wrong team case",
			teamID:          otherTeamID,
			ownerID:         otherOwnerID,
			subjectEntityID: foreignSubject.EntityID,
			objectEntityID:  foreignObject.EntityID,
			context:         V2PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
			beforeProfileID: otherOwnerID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := loadV2ConflictContextCommitCounts(t, ctx, appDB, rls, tc.teamID, tc.beforeProfileID)
			_, err := attemptV2PlacementRelationshipForConflictTest(
				t, ctx, ledgerRepo, tc.teamID, tc.ownerID, "worker-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				"context-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				"Conflicting evidence should not commit.", tc.subjectEntityID, tc.objectEntityID, "source-group-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				&tc.context,
			)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrV2ConflictContextStale), err)
			after := loadV2ConflictContextCommitCounts(t, ctx, appDB, rls, tc.teamID, tc.beforeProfileID)
			assert.Equal(t, before, after)
		})
	}

	dismissedVersion := dismissV2ConflictCaseForTest(t, ctx, appDB, rls, teamID, ownerA, conflictID)
	beforeDismissed := loadV2ConflictContextCommitCounts(t, ctx, appDB, rls, teamID, ownerA)
	_, err = attemptV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-invalid-dismissed",
		"context-invalid-dismissed", "Dismissed conflict evidence should not commit.", subject.EntityID, postgres.EntityID, "source-group-invalid-dismissed",
		&V2PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: dismissedVersion},
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2ConflictContextStale), err)
	afterDismissed := loadV2ConflictContextCommitCounts(t, ctx, appDB, rls, teamID, ownerA)
	assert.Equal(t, beforeDismissed, afterDismissed)
}

func TestV2RelationshipConflictReviewerResolvesMajorityAndSupersedesLosers(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "conflict-review", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "conflict-review-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-b")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-c")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	preferredA := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-review-a",
		"review-conflict-a", "Dense-Mem uses PostgreSQL according to team A.", subject.EntityID, postgres.EntityID, "source-group-review-a",
	)
	loser := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-review-b",
		"review-conflict-b", "Dense-Mem uses GraphDB according to team B.", subject.EntityID, graphdb.EntityID, "source-group-review-b",
	)
	preferredC := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-review-c",
		"review-conflict-c", "Dense-Mem uses PostgreSQL according to team C.", subject.EntityID, postgres.EntityID, "source-group-review-c",
	)

	var conflictID string
	var dueAt time.Time
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&conflictID, &dueAt)
	})
	require.NoError(t, err)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledgerRepo.ReserveV2RelationshipConflictReviewRun(ctx, V2ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)

	cases, err := ledgerRepo.ClaimV2RelationshipConflictCases(ctx, V2ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer",
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	assert.Equal(t, conflictID, cases[0].ConflictID)

	result, err := ledgerRepo.ReviewV2RelationshipConflictCase(ctx, V2ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, V2ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "due_majority", result.Stage)
	assert.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)
	require.NoError(t, ledgerRepo.CompleteV2RelationshipConflictReviewRun(ctx, V2ConflictReviewRunCompleteInput{
		TeamID:        teamID,
		ReviewRunID:   run.ReviewRunID,
		WorkerID:      "conflict-reviewer",
		Status:        "completed",
		ClaimedCases:  1,
		ResolvedCases: 1,
	}))

	var conflictStatus, loserStatus, preferredAStatus, preferredCStatus string
	var preferredPositionCount, suppressedPositionCount, transitionCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&conflictStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&loserStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, preferredA.RelationshipResults[0].Relationship.RelationshipID).Scan(&preferredAStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, preferredC.RelationshipResults[0].Relationship.RelationshipID).Scan(&preferredCStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND disposition = 'preferred'
		`, teamID, conflictID).Scan(&preferredPositionCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND disposition = 'suppressed_current'
		`, teamID, conflictID).Scan(&suppressedPositionCount).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_transition_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND reason = 'conflict_resolved'
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&transitionCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "resolved", conflictStatus)
	assert.Equal(t, "superseded", loserStatus)
	assert.Equal(t, "active", preferredAStatus)
	assert.Equal(t, "active", preferredCStatus)
	assert.Equal(t, int64(1), preferredPositionCount)
	assert.Equal(t, int64(1), suppressedPositionCount)
	assert.Equal(t, int64(1), transitionCount)

	searchRepo := NewV2SearchRepository(appDB, rls)
	currentRecall, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID: teamID,
		Query:  "PostgreSQL",
		Limit:  10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, currentRecall.Results)
	require.Len(t, currentRecall.Conflicts, 1)
	assert.Equal(t, "resolved", currentRecall.Conflicts[0].Status)
	assert.Equal(t, conflictID, currentRecall.Conflicts[0].ConflictID)
	assert.NotEmpty(t, currentRecall.Conflicts[0].PreferredPositionID)

	historicalValidAt := reviewNow.Add(-time.Minute)
	historicalRecall, err := searchRepo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID:  teamID,
		Query:   "GraphDB",
		Limit:   10,
		ValidAt: &historicalValidAt,
	})
	require.NoError(t, err)
	require.NotEmpty(t, historicalRecall.Results)
	historicalRelationshipIDs := []string{}
	for _, hit := range historicalRecall.Results {
		historicalRelationshipIDs = append(historicalRelationshipIDs, hit.RelationshipIDs...)
	}
	assert.Contains(t, historicalRelationshipIDs, loser.RelationshipResults[0].Relationship.RelationshipID)

	trace, err := semanticRepo.TraceRelationship(ctx, V2TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: loser.RelationshipResults[0].Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.Conflicts, 1)
	assert.Equal(t, "resolved", trace.Conflicts[0].Status)
	assert.Equal(t, conflictID, trace.Conflicts[0].ConflictID)
}

func TestV2RelationshipConflictReviewerClampsLoserValidToToItsValidFrom(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "conflict-review-valid-window", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "conflict-review-valid-window-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-valid-window-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-valid-window-owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	winnerValidFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	loserValidFrom := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	winner := commitV2PlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-valid-window-a",
		"valid-window-conflict-a", "Dense-Mem uses PostgreSQL according to the authoritative source.",
		subject.EntityID, postgres.EntityID, "source-group-valid-window-a",
		v2ConflictTestRelationshipOptions{validFrom: &winnerValidFrom, authority: "authoritative"},
	)
	loser := commitV2PlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-valid-window-b",
		"valid-window-conflict-b", "Dense-Mem uses GraphDB according to a later primary source.",
		subject.EntityID, graphdb.EntityID, "source-group-valid-window-b",
		v2ConflictTestRelationshipOptions{validFrom: &loserValidFrom, authority: "primary"},
	)

	var conflictID string
	var dueAt time.Time
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&conflictID, &dueAt)
	})
	require.NoError(t, err)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledgerRepo.ReserveV2RelationshipConflictReviewRun(ctx, V2ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer-valid-window",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = ledgerRepo.ClaimV2RelationshipConflictCases(ctx, V2ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-valid-window",
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)

	result, err := ledgerRepo.ReviewV2RelationshipConflictCase(ctx, V2ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-valid-window",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, V2ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "due_unique_authoritative", result.Stage)
	assert.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)

	var winnerStatus, loserStatus string
	var loserValidTo time.Time
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, winner.RelationshipResults[0].Relationship.RelationshipID).Scan(&winnerStatus).Error)
		return tx.Raw(`
			SELECT status, valid_to
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&loserStatus, &loserValidTo)
	})
	require.NoError(t, err)
	assert.Equal(t, "active", winnerStatus)
	assert.Equal(t, "superseded", loserStatus)
	assert.True(t, loserValidTo.Equal(loserValidFrom), "loser valid_to = %s, want %s", loserValidTo, loserValidFrom)
}

func TestV2RelationshipConflictReviewerDeduplicatesCopiedSourceGroups(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "conflict-review-dedupe", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "conflict-review-source-dedupe-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-b")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-c")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	preferredA := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-dedupe-a",
		"dedupe-conflict-a", "Dense-Mem uses PostgreSQL from the copied source.", subject.EntityID, postgres.EntityID, "copied-source-group",
	)
	loser := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-dedupe-b",
		"dedupe-conflict-b", "Dense-Mem uses GraphDB from an independent source.", subject.EntityID, graphdb.EntityID, "independent-source-group",
	)
	preferredC := commitV2PlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-dedupe-c",
		"dedupe-conflict-c", "Dense-Mem uses PostgreSQL from a copied source.", subject.EntityID, postgres.EntityID, "copied-source-group",
	)

	var conflictID string
	var dueAt time.Time
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&conflictID, &dueAt)
	})
	require.NoError(t, err)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledgerRepo.ReserveV2RelationshipConflictReviewRun(ctx, V2ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer-dedupe",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	cases, err := ledgerRepo.ClaimV2RelationshipConflictCases(ctx, V2ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-dedupe",
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	result, err := ledgerRepo.ReviewV2RelationshipConflictCase(ctx, V2ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-dedupe",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, V2ConflictReviewOutcomeOverdue, result.Outcome)
	assert.Equal(t, "due_no_winner", result.Stage)

	var conflictStatus, preferredAStatus, loserStatus, preferredCStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&conflictStatus).Error)
		for id, target := range map[string]*string{
			preferredA.RelationshipResults[0].Relationship.RelationshipID: &preferredAStatus,
			loser.RelationshipResults[0].Relationship.RelationshipID:      &loserStatus,
			preferredC.RelationshipResults[0].Relationship.RelationshipID: &preferredCStatus,
		} {
			require.NoError(t, tx.Raw(`
				SELECT status
				FROM relationship_records
				WHERE team_id = ?::uuid
				  AND relationship_id = ?::uuid
			`, teamID, id).Scan(target).Error)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "overdue", conflictStatus)
	assert.Equal(t, "active", preferredAStatus)
	assert.Equal(t, "active", loserStatus)
	assert.Equal(t, "active", preferredCStatus)
}

func commitV2PlacementRelationshipForConflictTest(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
) *V2CommitPlacementSemanticResult {
	t.Helper()
	committed, err := attemptV2PlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerID, workerID, idempotencyKey, content,
		subjectEntityID, objectEntityID, sourceGroupKey, nil,
	)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	return committed
}

type v2ConflictTestRelationshipOptions struct {
	validFrom *time.Time
	authority string
}

func commitV2PlacementRelationshipForConflictTestWithOptions(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
	options v2ConflictTestRelationshipOptions,
) *V2CommitPlacementSemanticResult {
	t.Helper()
	committed, err := attemptV2PlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerID, workerID, idempotencyKey, content,
		subjectEntityID, objectEntityID, sourceGroupKey, nil, options,
	)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	return committed
}

func attemptV2PlacementRelationshipForConflictTest(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
	conflictContext *V2PlacementConflictContextInput,
	options ...v2ConflictTestRelationshipOptions,
) (*V2CommitPlacementSemanticResult, error) {
	t.Helper()
	option := v2ConflictTestRelationshipOptions{authority: "primary"}
	if len(options) > 0 {
		option = options[0]
		if option.authority == "" {
			option.authority = "primary"
		}
	}
	ingest := createV2SemanticIngest(t, ctx, repo, teamID, ownerID, idempotencyKey, content)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, workerID, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	return repo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         workerID,
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subjectEntityID},
			{MentionRef: "object", Action: "reuse", EntityID: objectEntityID},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:          idempotencyKey + "-relationship",
			SubjectRef:   "subject",
			PredicateKey: "primary_database",
			ObjectRef:    "object",
			ValidFrom:    option.validFrom,
			ConflictContext: func() *V2PlacementConflictContextInput {
				if conflictContext == nil {
					return nil
				}
				context := *conflictContext
				return &context
			}(),
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: sourceGroupKey,
				SpanStart:      0,
				SpanEnd:        len(content),
				Quote:          content,
				Authority:      option.authority,
			},
		}},
	})
}

func loadV2ConflictCaseVersionForSubject(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls v2RLSHelper,
	teamID string,
	profileID string,
	subjectEntityID string,
) (string, int) {
	t.Helper()
	var conflictID string
	var version int
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND subject_entity_id = ?::uuid
			  AND status IN ('open', 'overdue')
			ORDER BY created_at DESC
			LIMIT 1
		`, teamID, subjectEntityID).Row().Scan(&conflictID, &version)
	}))
	require.NotEmpty(t, conflictID)
	require.GreaterOrEqual(t, version, 1)
	return conflictID, version
}

type v2ConflictContextCommitCounts struct {
	Relationships int64
	Supports      int64
	Outcomes      int64
}

func loadV2ConflictContextCommitCounts(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls v2RLSHelper,
	teamID string,
	profileID string,
) v2ConflictContextCommitCounts {
	t.Helper()
	var counts v2ConflictContextCommitCounts
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_records
			WHERE team_id = ?::uuid
		`, teamID).Scan(&counts.Relationships).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid
		`, teamID).Scan(&counts.Supports).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
		`, teamID).Scan(&counts.Outcomes).Error
	}))
	return counts
}

func dismissV2ConflictCaseForTest(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls v2RLSHelper,
	teamID string,
	profileID string,
	conflictID string,
) int {
	t.Helper()
	var version int
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			UPDATE relationship_conflict_cases
			SET status = 'dismissed',
			    version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			RETURNING version
		`, teamID, conflictID).Row().Scan(&version)
	}))
	require.GreaterOrEqual(t, version, 1)
	return version
}
