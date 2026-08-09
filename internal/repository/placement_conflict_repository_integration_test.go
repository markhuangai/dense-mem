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

func TestPlacementSemanticCommitOpensCrossProfileConflictCase(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "placement-conflict", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "placement-conflict-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "placement-conflict-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "placement-conflict-owner-b")
	ledgerRepo := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, 20, ConflictRuntimeConfig{
		ReviewTTLDays: 2,
		Timezone:      "UTC",
	})
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	first := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-conflict-a",
		"conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-a",
	)
	second := commitPlacementRelationshipForConflictTest(
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

	var conflicts []RelationshipConflictCaseRecord
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		var loadErr error
		conflicts, loadErr = loadRelationshipConflictRecords(ctx, tx, teamID, []string{
			first.RelationshipResults[0].Relationship.RelationshipID,
			second.RelationshipResults[0].Relationship.RelationshipID,
		}, nil)
		return loadErr
	})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	require.Len(t, conflicts[0].Positions, 2)
	assert.Equal(t, "open", conflicts[0].Status)
	for _, position := range conflicts[0].Positions {
		assert.True(t, position.RecordedFallback)
		assert.Equal(t, "recorded_at", position.EffectiveTimeBasis)
	}
	assert.ElementsMatch(t, []string{
		first.RelationshipResults[0].Relationship.RelationshipID,
		second.RelationshipResults[0].Relationship.RelationshipID,
	}, append(conflicts[0].Positions[0].RelationshipIDs, conflicts[0].Positions[1].RelationshipIDs...))

	searchRepo := NewSearchRepository(appDB, rls)
	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense-Mem",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, recall.Conflicts, 1)
	assert.Equal(t, conflictID, recall.Conflicts[0].ConflictID)
	assert.Len(t, recall.Conflicts[0].Positions, 2)
}

func TestPlacementSemanticCommitValidatesConflictContextAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "placement-conflict-context", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "placement-conflict-context-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "placement-conflict-context-owner-c")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "placement-conflict-context-other-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "placement-conflict-context-other-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	otherSubject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Other System")
	redis := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Redis")
	memcached := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Memcached")
	foreignSubject := createSemanticEntity(t, ctx, semanticRepo, otherTeamID, otherOwnerID, "project", "Foreign System")
	foreignObject := createSemanticEntity(t, ctx, semanticRepo, otherTeamID, otherOwnerID, "product", "SQLite")

	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-context-a",
		"context-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-context-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-context-b",
		"context-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-context-b",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-unrelated-a",
		"context-unrelated-a", "Other System uses Redis.", otherSubject.EntityID, redis.EntityID, "source-group-unrelated-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-unrelated-b",
		"context-unrelated-b", "Other System uses Memcached.", otherSubject.EntityID, memcached.EntityID, "source-group-unrelated-b",
	)

	conflictID, conflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	unrelatedConflictID, unrelatedConflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, otherSubject.EntityID)
	valid, err := attemptPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-context-c",
		"context-conflict-c", "Dense-Mem uses PostgreSQL according to owner C.", subject.EntityID, postgres.EntityID, "source-group-context-c",
		&PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
	)
	require.NoError(t, err)
	require.Len(t, valid.RelationshipResults, 1)
	assert.NotNil(t, valid.RelationshipResults[0].Relationship)
	_, refreshedConflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	assert.Greater(t, refreshedConflictVersion, conflictVersion)

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
		context         PlacementConflictContextInput
		beforeProfileID string
	}{
		{
			name:            "stale version",
			teamID:          teamID,
			ownerID:         ownerC,
			subjectEntityID: subject.EntityID,
			objectEntityID:  postgres.EntityID,
			context:         PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
			beforeProfileID: ownerA,
		},
		{
			name:            "unrelated same-team case",
			teamID:          teamID,
			ownerID:         ownerC,
			subjectEntityID: subject.EntityID,
			objectEntityID:  postgres.EntityID,
			context:         PlacementConflictContextInput{ConflictID: unrelatedConflictID, ExpectedVersion: unrelatedConflictVersion},
			beforeProfileID: ownerA,
		},
		{
			name:            "wrong team case",
			teamID:          otherTeamID,
			ownerID:         otherOwnerID,
			subjectEntityID: foreignSubject.EntityID,
			objectEntityID:  foreignObject.EntityID,
			context:         PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
			beforeProfileID: otherOwnerID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := loadConflictContextCommitCounts(t, ctx, appDB, rls, tc.teamID, tc.beforeProfileID)
			_, err := attemptPlacementRelationshipForConflictTest(
				t, ctx, ledgerRepo, tc.teamID, tc.ownerID, "worker-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				"context-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				"Conflicting evidence should not commit.", tc.subjectEntityID, tc.objectEntityID, "source-group-invalid-"+strings.ReplaceAll(tc.name, " ", "-"),
				&tc.context,
			)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrConflictContextStale), err)
			after := loadConflictContextCommitCounts(t, ctx, appDB, rls, tc.teamID, tc.beforeProfileID)
			assert.Equal(t, before, after)
		})
	}

	dismissedVersion := dismissConflictCaseForTest(t, ctx, appDB, rls, teamID, ownerA, conflictID)
	beforeDismissed := loadConflictContextCommitCounts(t, ctx, appDB, rls, teamID, ownerA)
	_, err = attemptPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-invalid-dismissed",
		"context-invalid-dismissed", "Dismissed conflict evidence should not commit.", subject.EntityID, postgres.EntityID, "source-group-invalid-dismissed",
		&PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: dismissedVersion},
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflictContextStale), err)
	afterDismissed := loadConflictContextCommitCounts(t, ctx, appDB, rls, teamID, ownerA)
	assert.Equal(t, beforeDismissed, afterDismissed)
}

func TestRelationshipConflictReviewerResolvesMajorityAndSupersedesLosers(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	preferredA := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-review-a",
		"review-conflict-a", "Dense-Mem uses PostgreSQL according to team A.", subject.EntityID, postgres.EntityID, "source-group-review-a",
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-review-b",
		"review-conflict-b", "Dense-Mem uses GraphDB according to team B.", subject.EntityID, graphdb.EntityID, "source-group-review-b",
	)
	preferredC := commitPlacementRelationshipForConflictTest(
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
	run, claimed, err := ledgerRepo.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)

	cases, err := ledgerRepo.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
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

	result, err := ledgerRepo.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "due_supporter_majority", result.Stage)
	assert.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)
	require.NoError(t, ledgerRepo.CompleteRelationshipConflictReviewRun(ctx, ConflictReviewRunCompleteInput{
		TeamID:        teamID,
		ReviewRunID:   run.ReviewRunID,
		WorkerID:      "conflict-reviewer",
		Status:        "completed",
		ClaimedCases:  1,
		ResolvedCases: 1,
	}))

	var conflictStatus, loserStatus, preferredAStatus, preferredCStatus, loserSearchState string
	var loserRelationshipVersion, loserSearchSourceVersion int64
	var preferredPositionCount, suppressedPositionCount, transitionCount, staleEmbeddingJobs, queuedEmbeddingJobs int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&conflictStatus).Error)
		require.NoError(t, tx.Raw(`
			SELECT status, version
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&loserStatus, &loserRelationshipVersion))
		require.NoError(t, tx.Raw(`
			SELECT source_version, search_state
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&loserSearchSourceVersion, &loserSearchState))
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ?::uuid
			  AND status = 'stale'
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&staleEmbeddingJobs).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ?::uuid
			  AND source_version = ?
			  AND status = 'queued'
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID, loserRelationshipVersion).Scan(&queuedEmbeddingJobs).Error)
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
	assert.Equal(t, loserRelationshipVersion, loserSearchSourceVersion)
	assert.Equal(t, "not_required", loserSearchState)
	assert.GreaterOrEqual(t, staleEmbeddingJobs, int64(1))
	assert.Equal(t, int64(0), queuedEmbeddingJobs)
	assert.Equal(t, "active", preferredAStatus)
	assert.Equal(t, "active", preferredCStatus)
	assert.Equal(t, int64(1), preferredPositionCount)
	assert.Equal(t, int64(1), suppressedPositionCount)
	assert.Equal(t, int64(1), transitionCount)

	searchRepo := NewSearchRepository(appDB, rls)
	currentRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
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
	historicalRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
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
	historicalKnownAt := reviewNow.Add(-time.Second)
	var historicalConflicts []RelationshipConflictCaseRecord
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		var loadErr error
		historicalConflicts, loadErr = loadRelationshipConflictRecords(ctx, tx, teamID, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, &historicalKnownAt)
		return loadErr
	}))
	require.Len(t, historicalConflicts, 1)
	assert.NotEqual(t, "resolved", historicalConflicts[0].Status)
	assert.Empty(t, historicalConflicts[0].PreferredPositionID)
	assert.False(t, historicalConflicts[0].NextReviewAt.After(historicalKnownAt))
	require.Len(t, historicalConflicts[0].Positions, 2)
	for _, position := range historicalConflicts[0].Positions {
		assert.Equal(t, "candidate", position.Disposition)
	}

	trace, err := semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: loser.RelationshipResults[0].Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.Conflicts, 1)
	assert.Equal(t, "resolved", trace.Conflicts[0].Status)
	assert.Equal(t, conflictID, trace.Conflicts[0].ConflictID)
}

func TestRelationshipConflictReviewerClampsLoserValidToToItsValidFrom(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-valid-window", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-valid-window-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-valid-window-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-valid-window-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-valid-window-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	winnerValidFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	loserValidFrom := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	winner := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-valid-window-a",
		"valid-window-conflict-a", "Dense-Mem uses PostgreSQL according to the authoritative source.",
		subject.EntityID, postgres.EntityID, "source-group-valid-window-a",
		conflictTestRelationshipOptions{validFrom: &winnerValidFrom, authority: "authoritative"},
	)
	loser := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-valid-window-b",
		"valid-window-conflict-b", "Dense-Mem uses GraphDB according to a later primary source.",
		subject.EntityID, graphdb.EntityID, "source-group-valid-window-b",
		conflictTestRelationshipOptions{validFrom: &loserValidFrom, authority: "primary"},
	)
	_ = commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-valid-window-c",
		"valid-window-conflict-c", "Dense-Mem uses PostgreSQL according to another supporting source.",
		subject.EntityID, postgres.EntityID, "source-group-valid-window-c",
		conflictTestRelationshipOptions{validFrom: &winnerValidFrom, authority: "secondary"},
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
	run, claimed, err := ledgerRepo.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer-valid-window",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = ledgerRepo.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-valid-window",
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)

	result, err := ledgerRepo.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-valid-window",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "due_supporter_majority", result.Stage)
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

func TestRelationshipConflictReviewerCountsSupportersAcrossCopiedSourceGroups(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-dedupe", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-source-dedupe-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-source-dedupe-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	preferredA := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-dedupe-a",
		"dedupe-conflict-a", "Dense-Mem uses PostgreSQL from the copied source.", subject.EntityID, postgres.EntityID, "copied-source-group",
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-dedupe-b",
		"dedupe-conflict-b", "Dense-Mem uses GraphDB from an independent source.", subject.EntityID, graphdb.EntityID, "independent-source-group",
	)
	preferredC := commitPlacementRelationshipForConflictTest(
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
	run, claimed, err := ledgerRepo.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     "conflict-reviewer-dedupe",
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	cases, err := ledgerRepo.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
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

	result, err := ledgerRepo.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    "conflict-reviewer-dedupe",
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	assert.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "due_supporter_majority", result.Stage)

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
	assert.Equal(t, "resolved", conflictStatus)
	assert.Equal(t, "active", preferredAStatus)
	assert.Equal(t, "superseded", loserStatus)
	assert.Equal(t, "active", preferredCStatus)
}

func commitPlacementRelationshipForConflictTest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
) *CommitPlacementSemanticResult {
	t.Helper()
	committed, err := attemptPlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerID, workerID, idempotencyKey, content,
		subjectEntityID, objectEntityID, sourceGroupKey, nil,
	)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	return committed
}

type conflictTestRelationshipOptions struct {
	validFrom          *time.Time
	authority          string
	additionalSupports []conflictTestAdditionalSupport
}

type conflictTestAdditionalSupport struct {
	sourceGroupKey string
	spanStart      int
	spanEnd        int
	quote          string
	authority      string
}

func commitPlacementRelationshipForConflictTestWithOptions(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
	options conflictTestRelationshipOptions,
) *CommitPlacementSemanticResult {
	t.Helper()
	committed, err := attemptPlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerID, workerID, idempotencyKey, content,
		subjectEntityID, objectEntityID, sourceGroupKey, nil, options,
	)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	return committed
}

func attemptPlacementRelationshipForConflictTest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
	conflictContext *PlacementConflictContextInput,
	options ...conflictTestRelationshipOptions,
) (*CommitPlacementSemanticResult, error) {
	t.Helper()
	option := conflictTestRelationshipOptions{authority: "primary"}
	if len(options) > 0 {
		option = options[0]
		if option.authority == "" {
			option.authority = "primary"
		}
	}
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, idempotencyKey, content)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, workerID, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	additionalSupports := make([]EvidenceSupportInput, 0, len(option.additionalSupports))
	for _, support := range option.additionalSupports {
		authority := support.authority
		if authority == "" {
			authority = option.authority
		}
		additionalSupports = append(additionalSupports, EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: support.sourceGroupKey,
			SpanStart:      support.spanStart,
			SpanEnd:        support.spanEnd,
			Quote:          support.quote,
			Authority:      authority,
		})
	}
	return repo.CommitPlacementSemanticResult(ctx, CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         workerID,
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subjectEntityID},
			{MentionRef: "object", Action: "reuse", EntityID: objectEntityID},
		},
		RelationshipObservations: []PlacementRelationshipDecisionInput{{
			Ref:          idempotencyKey + "-relationship",
			SubjectRef:   "subject",
			PredicateKey: "primary_database",
			ObjectRef:    "object",
			ValidFrom:    option.validFrom,
			ConflictContext: func() *PlacementConflictContextInput {
				if conflictContext == nil {
					return nil
				}
				context := *conflictContext
				return &context
			}(),
			Support: &EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: sourceGroupKey,
				SpanStart:      0,
				SpanEnd:        len(content),
				Quote:          content,
				Authority:      option.authority,
			},
			Supports: additionalSupports,
		}},
	})
}

func loadConflictCaseVersionForSubject(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
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

type conflictContextCommitCounts struct {
	Relationships int64
	Supports      int64
	Outcomes      int64
}

func loadConflictContextCommitCounts(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
	teamID string,
	profileID string,
) conflictContextCommitCounts {
	t.Helper()
	var counts conflictContextCommitCounts
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

func dismissConflictCaseForTest(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
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
