package repository

import (
	"context"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// ConflictResolutionStateSnapshot is exposed to co-located external tests
// without adding test fixture helpers to the production repository API.
type ConflictResolutionStateSnapshot = conflictResolutionStateSnapshot

// DeterministicConflictServiceFixture contains a real PostgreSQL conflict
// review claim prepared for the service-level embedding retry test.
type DeterministicConflictServiceFixture struct {
	Ledger          *LedgerRepositoryImpl
	TeamID          string
	ConflictID      string
	ReviewRunID     string
	WorkerID        string
	ReviewNow       time.Time
	RelationshipIDs []string
	adminDB         *gorm.DB
	rls             *postgres.RLS
}

// NewDeterministicConflictServiceFixture prepares a due conflict with a claimed review run.
func NewDeterministicConflictServiceFixture(t *testing.T) *DeterministicConflictServiceFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-resolution-deterministic-failure", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-resolution-deterministic-failure-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-deterministic-failure-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-deterministic-failure-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-deterministic-failure-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Deterministic failure subject")
	firstObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Deterministic failure first")
	secondObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Deterministic failure second")
	first := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-deterministic-failure-a",
		"deterministic-failure-a", "The first deterministic failure value is current.", subject.EntityID, firstObject.EntityID, "source-group-deterministic-failure-a",
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-deterministic-failure-b",
		"deterministic-failure-b", "The second deterministic failure value is current.", subject.EntityID, secondObject.EntityID, "source-group-deterministic-failure-b",
	)
	third := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-deterministic-failure-c",
		"deterministic-failure-c", "The first deterministic failure value has another supporter.", subject.EntityID, firstObject.EntityID, "source-group-deterministic-failure-c",
	)
	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?, next_review_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, reviewNow, reviewNow, teamID, conflictID).Error
	}))
	relationshipIDs := []string{
		first.RelationshipResults[0].Relationship.RelationshipID,
		loser.RelationshipResults[0].Relationship.RelationshipID,
		third.RelationshipResults[0].Relationship.RelationshipID,
	}
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding = NULL, embedding_error = 'forced deterministic embedding failure', updated_at = now()
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ANY(?::uuid[])
		`, teamID, pq.Array(relationshipIDs)).Error
	}))

	workerID := "worker-deterministic-failure"
	run, claimed, err := ledgerRepo.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: workerID, LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledgerRepo.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: workerID, ReviewRunID: run.ReviewRunID,
		Limit: 1, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	return &DeterministicConflictServiceFixture{
		Ledger: ledgerRepo,
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID, WorkerID: workerID,
		ReviewNow: reviewNow, RelationshipIDs: relationshipIDs, adminDB: adminDB, rls: rls,
	}
}

// Snapshot captures the durable state used to verify retryable failures are write-free.
func (f *DeterministicConflictServiceFixture) Snapshot(t *testing.T) ConflictResolutionStateSnapshot {
	t.Helper()
	return captureConflictResolutionStateSnapshot(t, context.Background(), f.adminDB, f.rls, f.TeamID, f.ConflictID, f.RelationshipIDs)
}
