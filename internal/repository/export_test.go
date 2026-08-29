package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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

// ExpireClaim makes the fixture's claimed conflict unavailable to the current worker.
func (f *DeterministicConflictServiceFixture) ExpireClaim(t *testing.T) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, f.TeamID, f.ConflictID).Error
	}))
}

// OversizedConflictServiceFixture prepares a due deterministic conflict whose
// selected position needs one document more than the synchronous embedding
// batch bound.
type OversizedConflictServiceFixture struct {
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

// NewOversizedConflictServiceFixture prepares a claimed conflict with 257
// distinct winner documents and one losing relationship.
func NewOversizedConflictServiceFixture(t *testing.T) *OversizedConflictServiceFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	prefix := "conflict-resolution-oversized"
	insertSearchTestContract(t, adminDB, rls, prefix, 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, prefix+"-team")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subjectOwner := createLedgerProfile(t, adminDB, rls, teamID, prefix+"-subject-owner")
	subject := createSemanticEntity(t, ctx, semantic, teamID, subjectOwner, "project", prefix+" subject")
	winnerObject := createSemanticEntity(t, ctx, semantic, teamID, subjectOwner, "product", prefix+" winner")
	loserObject := createSemanticEntity(t, ctx, semantic, teamID, subjectOwner, "product", prefix+" loser")

	validFromBase := time.Now().UTC().Add(-2 * time.Hour)
	firstOwner := createLedgerProfile(t, adminDB, rls, teamID, prefix+"-first-owner")
	firstValidFrom := validFromBase
	first := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledger, teamID, firstOwner, prefix+"-first-worker", prefix+"-first",
		prefix+" first winner relationship", subject.EntityID, winnerObject.EntityID, prefix+"-first-source",
		conflictTestRelationshipOptions{validFrom: &firstValidFrom},
	)
	loserOwner := createLedgerProfile(t, adminDB, rls, teamID, prefix+"-loser-owner")
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID, loserOwner, prefix+"-loser-worker", prefix+"-loser",
		prefix+" loser relationship", subject.EntityID, loserObject.EntityID, prefix+"-loser-source",
	)
	relationshipIDs := []string{
		first.RelationshipResults[0].Relationship.RelationshipID,
		loser.RelationshipResults[0].Relationship.RelationshipID,
	}
	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, subjectOwner, subject.EntityID)
	var winnerPositionID string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT position_id::text
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND object_entity_id = ?::uuid
		`, teamID, conflictID, winnerObject.EntityID).Row().Scan(&winnerPositionID)
	}))
	require.NotEmpty(t, winnerPositionID)

	type seed struct {
		ownerID   string
		ingest    *CreateIngestResult
		validFrom *time.Time
		content   string
		sourceKey string
	}
	seeds := make([]seed, 0, domain.MaxEmbeddingBatchDocuments)
	for index := 0; index < domain.MaxEmbeddingBatchDocuments; index++ {
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, fmt.Sprintf("%s-winner-owner-%03d", prefix, index))
		validFrom := validFromBase.Add(-time.Duration(index+1) * time.Minute)
		content := fmt.Sprintf("%s extra winner relationship %03d", prefix, index)
		ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, fmt.Sprintf("%s-winner-%03d", prefix, index), content)
		seeds = append(seeds, seed{
			ownerID: ownerID, ingest: ingest, validFrom: &validFrom, content: content,
			sourceKey: fmt.Sprintf("%s-winner-source-%03d", prefix, index),
		})
	}
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID); err != nil {
			return err
		}
		for index, item := range seeds {
			objectID := winnerObject.EntityID
			result, err := applyRelationshipDecisionInTx(ctx, tx, ApplyRelationshipDecisionInput{
				TeamID: teamID, OwnerProfileID: item.ownerID, IngestID: item.ingest.IngestID,
				SubjectRef: "subject", SubjectEntityID: subject.EntityID,
				OriginalPredicate: "primary_database", PredicateKey: "primary_database", PredicateVersion: 1,
				ObjectRef: "object", ObjectEntityID: objectID, Polarity: "+", ValidFrom: item.validFrom,
				EvidenceVerdict: string(domain.VerificationEntailed),
				Support: &EvidenceSupportInput{
					FragmentID: item.ingest.Evidence[0].FragmentID, SourceGroupKey: item.sourceKey,
					SpanStart: 0, SpanEnd: len(item.content), Quote: item.content, Authority: "primary",
				},
			})
			if err != nil {
				return err
			}
			if result == nil || result.Relationship == nil {
				return fmt.Errorf("oversized conflict fixture: relationship %d was not created", index)
			}
			if _, err := upsertPlacementRelationshipSearchDocument(ctx, tx, CommitPlacementSemanticInput{
				TeamID: teamID, OwnerProfileID: item.ownerID,
			}, result.Relationship, 5); err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO relationship_conflict_position_members (
					team_id, conflict_id, position_id, relationship_id, owner_profile_id,
					support_id, verification_event_id, fragment_id, source_group_key, authority,
					effective_at, effective_time_basis, recorded_fallback, active, accepted_at
				) VALUES (
					?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
					?::uuid, ?::uuid, ?::uuid, ?, 'primary',
					?, 'valid_from', false, true, ?
				)
			`, teamID, conflictID, winnerPositionID, result.Relationship.RelationshipID, item.ownerID,
				result.SupportID, result.VerificationEventID, item.ingest.Evidence[0].FragmentID,
				item.sourceKey, item.validFrom, *item.validFrom).Error; err != nil {
				return err
			}
			relationshipIDs = append(relationshipIDs, result.Relationship.RelationshipID)
		}
		return tx.Exec(`
			UPDATE relationship_conflict_positions AS position
			SET support_group_count = (
				SELECT COUNT(DISTINCT member.owner_profile_id)
				FROM relationship_conflict_position_members AS member
				WHERE member.team_id = position.team_id
				  AND member.position_id = position.position_id
				  AND member.active
			),
				authoritative_group_count = (
				SELECT COUNT(DISTINCT member.owner_profile_id)
				FROM relationship_conflict_position_members AS member
				WHERE member.team_id = position.team_id
				  AND member.position_id = position.position_id
				  AND member.active AND member.authority = 'authoritative'
			),
			updated_at = now(), last_seen_at = now()
			WHERE position.team_id = ?::uuid AND position.conflict_id = ?::uuid AND position.position_id = ?::uuid
		`, teamID, conflictID, winnerPositionID).Error
	}))
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, subjectOwner, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?, next_review_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	workerID := prefix + "-worker"
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: workerID, LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	claimedCases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: workerID, ReviewRunID: run.ReviewRunID,
		Limit: 1, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, claimedCases, 1)

	return &OversizedConflictServiceFixture{
		Ledger: ledger, TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID,
		WorkerID: workerID, ReviewNow: reviewNow, RelationshipIDs: relationshipIDs, adminDB: adminDB, rls: rls,
	}
}

// Snapshot captures the durable state used by the oversized fan-out regression.
func (f *OversizedConflictServiceFixture) Snapshot(t *testing.T) ConflictResolutionStateSnapshot {
	t.Helper()
	return captureConflictResolutionStateSnapshot(t, context.Background(), f.adminDB, f.rls, f.TeamID, f.ConflictID, f.RelationshipIDs)
}

// PendingState returns the pending-event count and next scheduled review time.
func (f *OversizedConflictServiceFixture) PendingState(t *testing.T) (int64, time.Time) {
	t.Helper()
	var eventCount int64
	var nextReviewAt time.Time
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT next_review_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, f.TeamID, f.ConflictID).Row().Scan(&nextReviewAt); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM relationship_conflict_events
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
			  AND action = 'resolution_pending' AND outcome = 'embedding_bound'
		`, f.TeamID, f.ConflictID).Row().Scan(&eventCount)
	}))
	return eventCount, nextReviewAt
}

// Reclaim makes the deferred case due again and claims it for an idempotency retry.
func (f *OversizedConflictServiceFixture) Reclaim(t *testing.T) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET next_review_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, f.ReviewNow, f.TeamID, f.ConflictID).Error
	}))
	claimed, err := f.Ledger.ClaimRelationshipConflictCases(context.Background(), ClaimRelationshipConflictCasesInput{
		TeamID: f.TeamID, WorkerID: f.WorkerID, ReviewRunID: f.ReviewRunID,
		Limit: 1, Lease: time.Minute, MaxAttempts: 5, Now: f.ReviewNow,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
}
