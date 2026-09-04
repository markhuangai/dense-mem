package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// Conflict resolution supersedes losing Relationships; it must retire their
// search projections without asking the embedding provider to render them.
func TestConflictReviewRetiresLosingSearchProjectionWithoutEmbedding(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-synchronous", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-synchronous-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-synchronous-owner-c")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Dense-Mem")
	preferredObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "PostgreSQL")
	losingObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "GraphDB")
	preferred := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerA, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL.", "conflict-preferred")
	loser := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerB, subject.EntityID, losingObject.EntityID, "Dense-Mem uses GraphDB.", "conflict-loser")
	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerC, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL for the team.", "conflict-preferred-second")
	require.NotEmpty(t, preferred.RelationshipResults)
	require.NotEmpty(t, loser.RelationshipResults)

	var conflictID string
	var dueAt time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &dueAt)
	}))
	require.NotEmpty(t, conflictID)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	result, err := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: teamID, WorkerID: "conflict-reviewer", ReviewRunID: run.ReviewRunID,
		ConflictID: conflictID, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Equal(t, ConflictReviewOutcomeResolve, result.Outcome)
	require.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)

	var loserStatus, searchState string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&loserStatus).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Scan(&searchState).Error
	}))
	require.Equal(t, string(domain.RelationshipStatusSuperseded), loserStatus)
	require.Equal(t, string(domain.SearchProjectionNotRequired), searchState)
}

func TestConflictSnapshotScopeSerializesPlacementReviewAndWrite(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	appSQL.SetMaxOpenConns(8)
	appSQL.SetMaxIdleConns(8)
	insertSearchTestContract(t, adminDB, rls, "conflict-snapshot-scope-lock", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-snapshot-scope-lock-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-snapshot-scope-lock-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-snapshot-scope-lock-owner-b")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Conflict snapshot scope")
	objectA := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "PostgreSQL")
	objectB := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "GraphDB")
	first := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerA, subject.EntityID, objectA.EntityID, "Conflict snapshot scope uses PostgreSQL.", "conflict-snapshot-scope-a")
	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerB, subject.EntityID, objectB.EntityID, "Conflict snapshot scope uses GraphDB.", "conflict-snapshot-scope-b")
	require.Len(t, first.RelationshipResults, 1)
	require.NotNil(t, first.RelationshipResults[0].Relationship)

	var conflictID, scopeKey string
	var dueAt time.Time
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT conflict_id::text, semantic_scope_key, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &scopeKey, &dueAt)
	}))
	require.NotEmpty(t, conflictID)
	require.NotEmpty(t, scopeKey)

	holdScopeLock := func() func() {
		ready := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
				if err := tx.WithContext(ctx).Exec(
					`SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))`,
					teamID+":relationship-conflict-snapshot:"+scopeKey,
				).Error; err != nil {
					return err
				}
				close(ready)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		select {
		case <-ready:
		case err := <-done:
			require.NoError(t, err)
			t.Fatal("conflict snapshot lock holder exited before acquiring the lock")
		case <-ctx.Done():
			t.Fatal("timed out acquiring the conflict snapshot lock")
		}
		released := false
		return func() {
			if released {
				return
			}
			released = true
			close(release)
			require.NoError(t, <-done)
		}
	}
	assertBlocked := func(label string, done <-chan error) {
		select {
		case err := <-done:
			t.Fatalf("%s did not wait for the conflict snapshot lock: %v", label, err)
		case <-time.After(150 * time.Millisecond):
		}
	}

	releasePlacement := holdScopeLock()
	defer releasePlacement()
	placementDone := make(chan error, 1)
	go func() {
		placementDone <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			placement, err := loadRelationshipConflictPlacement(ctx, tx, teamID, first.RelationshipResults[0].Relationship)
			if err != nil {
				return err
			}
			return upsertRelationshipConflictCase(ctx, tx, teamID, placement, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})
		})
	}()
	assertBlocked("conflict placement", placementDone)
	releasePlacement()
	require.NoError(t, <-placementDone)

	reviewNow := dueAt.Add(-time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "conflict-snapshot-scope-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "conflict-snapshot-scope-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	releaseReview := holdScopeLock()
	defer releaseReview()
	reviewDone := make(chan error, 1)
	go func() {
		_, reviewErr := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
			TeamID: teamID, WorkerID: "conflict-snapshot-scope-reviewer", ReviewRunID: run.ReviewRunID,
			ConflictID: conflictID, Now: reviewNow,
		})
		reviewDone <- reviewErr
	}()
	assertBlocked("conflict review", reviewDone)
	releaseReview()
	require.NoError(t, <-reviewDone)

	input := conflictRememberFixtureInput(teamID, ownerA, subject.EntityID, objectA.EntityID, "Conflict scope ordering adds more support.", "conflict-snapshot-scope-write", nil)
	vectors := conflictRememberFixtureVectors(t, ctx, ledger, input)
	releaseWrite := holdScopeLock()
	defer releaseWrite()
	rememberDone := make(chan error, 1)
	go func() {
		_, rememberErr := ledger.CommitRememberWithEmbeddings(ctx, input, vectors)
		rememberDone <- rememberErr
	}()

	scopeLockKey := teamID + ":relationship-conflict-snapshot:" + scopeKey
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	for {
		var waiting bool
		err := adminSQL.QueryRowContext(waitCtx, `
			WITH scope AS (
				SELECT hashtextextended($1::text, 0) AS advisory_key
			)
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks, scope
				WHERE locktype = 'advisory'
				  AND NOT granted
				  AND objsubid = 1
				  AND classid::bigint = ((scope.advisory_key >> 32) & 4294967295)
				  AND objid::bigint = (scope.advisory_key & 4294967295)
			)
		`, scopeLockKey).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case rememberErr := <-rememberDone:
			require.NoError(t, rememberErr, "Remember returned before waiting for the conflict snapshot scope")
			t.Fatal("Remember returned before waiting for the conflict snapshot scope")
		case <-waitCtx.Done():
			t.Fatal("Remember did not wait for the conflict snapshot scope")
		case <-time.After(10 * time.Millisecond):
		}
	}

	var lockedRelationshipID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT relationship_id::text
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			FOR UPDATE NOWAIT
		`, teamID, first.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&lockedRelationshipID)
	}))
	require.Equal(t, first.RelationshipResults[0].Relationship.RelationshipID, lockedRelationshipID)

	releaseWrite()
	require.NoError(t, <-rememberDone)
}

func TestConflictSnapshotScopeLocksCorrectionBeforeReviewRowLock(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	appSQL.SetMaxOpenConns(8)
	appSQL.SetMaxIdleConns(8)
	insertSearchTestContract(t, adminDB, rls, "conflict-correction-scope-lock", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-correction-scope-lock-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-correction-scope-lock-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-correction-scope-lock-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-correction-scope-lock-owner-c")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Correction scope lock")
	objectA := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "PostgreSQL")
	objectB := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "GraphDB")
	correctedObject := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "product", "SQLite")
	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerA, subject.EntityID, objectA.EntityID, "Correction scope lock uses PostgreSQL.", "conflict-correction-scope-a")
	source := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerB, subject.EntityID, objectB.EntityID, "Correction scope lock uses GraphDB.", "conflict-correction-scope-b")
	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerC, subject.EntityID, objectA.EntityID, "Correction scope lock uses PostgreSQL again.", "conflict-correction-scope-c")
	relationship := source.RelationshipResults[0].Relationship
	require.NotNil(t, relationship)

	var conflictID, scopeKey, evidenceID string
	var dueAt time.Time
	var supportStart, supportEnd int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT conflict_id::text, semantic_scope_key, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &scopeKey, &dueAt); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT fragment_id::text, span_start, span_end
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			ORDER BY created_at, support_id
			LIMIT 1
		`, teamID, relationship.RelationshipID).Row().Scan(&evidenceID, &supportStart, &supportEnd)
	}))
	require.NotEmpty(t, conflictID)
	require.NotEmpty(t, scopeKey)

	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "conflict-correction-scope-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "conflict-correction-scope-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	correction := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerB, Action: "submit",
		RelationshipID: relationship.RelationshipID, ExpectedVersion: relationship.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctedObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: evidenceID, Start: supportStart, End: supportEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "conflict-correction-scope-lock",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, correction)
	require.NoError(t, err)

	caseLocked := make(chan struct{})
	releaseCase := make(chan struct{})
	caseLockDone := make(chan error, 1)
	go func() {
		caseLockDone <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			var lockedConflictID string
			if err := tx.Raw(`
				SELECT conflict_id::text
				FROM relationship_conflict_cases
				WHERE team_id = ?::uuid AND conflict_id = ?::uuid
				FOR UPDATE
			`, teamID, conflictID).Row().Scan(&lockedConflictID); err != nil {
				return err
			}
			close(caseLocked)
			select {
			case <-releaseCase:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()
	select {
	case <-caseLocked:
	case err := <-caseLockDone:
		require.NoError(t, err)
		t.Fatal("review transaction exited before locking the conflict case")
	case <-ctx.Done():
		t.Fatal("timed out locking the conflict case")
	}
	caseReleased := false
	defer func() {
		if !caseReleased {
			close(releaseCase)
		}
	}()

	type reviewResult struct {
		result *ReviewRelationshipConflictCaseResult
		err    error
	}
	reviewDone := make(chan reviewResult, 1)
	go func() {
		result, reviewErr := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
			TeamID: teamID, WorkerID: "conflict-correction-scope-reviewer", ReviewRunID: run.ReviewRunID,
			ConflictID: conflictID, Now: reviewNow,
		})
		reviewDone <- reviewResult{result: result, err: reviewErr}
	}()
	reviewWaitCtx, reviewWaitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reviewWaitCancel()
	for {
		var waiting bool
		err := adminSQL.QueryRowContext(reviewWaitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE locktype = 'transactionid' AND NOT granted
			)
		`).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case outcome := <-reviewDone:
			require.NoError(t, outcome.err, "review returned before waiting for the conflict-case lock")
			t.Fatal("review returned before waiting for the conflict-case lock")
		case <-reviewWaitCtx.Done():
			t.Fatal("review did not acquire the conflict snapshot scope before waiting for the conflict case")
		case <-time.After(10 * time.Millisecond):
		}
	}

	type correctionResult struct {
		result *CorrectRelationshipResult
		err    error
	}
	correctionDone := make(chan correctionResult, 1)
	go func() {
		result, correctionErr := semantic.CorrectRelationshipWithEmbeddings(ctx, correction, relationshipCorrectionTestEmbeddings(plan))
		correctionDone <- correctionResult{result: result, err: correctionErr}
	}()

	scopeLockKey := teamID + ":relationship-conflict-snapshot:" + scopeKey
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	for {
		var waiting bool
		err := adminSQL.QueryRowContext(waitCtx, `
			WITH scope AS (
				SELECT hashtextextended($1::text, 0) AS advisory_key
			)
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks, scope
				WHERE locktype = 'advisory'
				  AND NOT granted
				  AND objsubid = 1
				  AND classid::bigint = ((scope.advisory_key >> 32) & 4294967295)
				  AND objid::bigint = (scope.advisory_key & 4294967295)
			)
		`, scopeLockKey).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case outcome := <-correctionDone:
			require.NoError(t, outcome.err, "correction returned before waiting for the conflict snapshot scope")
			t.Fatal("correction returned before waiting for the conflict snapshot scope")
		case <-waitCtx.Done():
			t.Fatal("correction did not wait for the conflict snapshot scope")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(releaseCase)
	caseReleased = true
	select {
	case err := <-caseLockDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("conflict-case lock did not release")
	}
	select {
	case outcome := <-reviewDone:
		require.NoError(t, outcome.err)
		require.Equal(t, ConflictReviewOutcomeResolve, outcome.result.Outcome)
		require.Contains(t, outcome.result.UpdatedRelationships, relationship.RelationshipID)
	case <-ctx.Done():
		t.Fatal("review did not complete after the conflict-case lock released")
	}
	select {
	case outcome := <-correctionDone:
		require.NoError(t, outcome.err)
		require.Equal(t, "rejected", outcome.result.ProcessingState)
		require.Equal(t, "relationship_version_stale", outcome.result.ErrorCode)
	case <-ctx.Done():
		t.Fatal("correction did not complete after the review released the conflict snapshot scope")
	}
}

func TestOverdueConflictResolutionRetainsEvidenceUsedByAnotherOwner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-cross-owner-support", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-cross-owner-support-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-cross-owner-support-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-cross-owner-support-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-cross-owner-support-owner-c")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Cross-owner conflict subject")
	preferredObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Cross-owner preferred object")
	losingObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Cross-owner losing object")
	unrelatedSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Cross-owner unrelated subject")
	unrelatedObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Cross-owner unrelated object")
	loser := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerA, subject.EntityID, losingObject.EntityID, "Cross-owner conflict subject uses the losing object.", "cross-owner-conflict-loser")
	var knownFragmentID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT fragment_id::text
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			ORDER BY created_at, support_id
			LIMIT 1
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&knownFragmentID)
	}))
	require.NotEmpty(t, knownFragmentID)
	knownContent := "Cross-owner conflict subject uses the losing object."

	unrelatedSubmitted := createSemanticIngest(t, ctx, ledger, teamID, ownerB,
		"cross-owner-unrelated-submitted", "Owner B's relationship uses owner A's known evidence.")
	unrelated := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerB, IngestID: unrelatedSubmitted.IngestID,
		SubjectEntityID: unrelatedSubject.EntityID, PredicateKey: "uses", ObjectEntityID: unrelatedObject.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support: &EvidenceSupportInput{
			FragmentID: unrelatedSubmitted.Evidence[0].FragmentID, SourceGroupKey: "cross-owner-unrelated-submitted",
			SpanStart: 0, SpanEnd: len([]rune(unrelatedSubmitted.Evidence[0].Content)), Authority: "primary",
		},
		Supports: []EvidenceSupportInput{{
			FragmentID: knownFragmentID, EvidenceOwnerProfileID: ownerA, SourceGroupKey: "cross-owner-known-support",
			SpanStart: 0, SpanEnd: len([]rune(knownContent)), Quote: knownContent, Authority: "primary",
		}},
	})
	require.NotNil(t, unrelated.Relationship)

	_ = commitConflictRememberFixture(t, ctx, ledger, teamID, ownerC, subject.EntityID, preferredObject.EntityID, "Cross-owner conflict subject uses the preferred object.", "cross-owner-conflict-preferred")

	var conflictID string
	var dueAt time.Time
	var preferredPositionID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &dueAt); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT position_id::text
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND object_entity_id = ?::uuid
		`, teamID, conflictID, preferredObject.EntityID).Row().Scan(&preferredPositionID)
	}))
	require.NotEmpty(t, conflictID)
	require.NotEmpty(t, preferredPositionID)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "cross-owner-conflict-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "cross-owner-conflict-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	review, err := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: teamID, WorkerID: "cross-owner-conflict-reviewer", ReviewRunID: run.ReviewRunID,
		ConflictID: conflictID, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Equal(t, ConflictReviewOutcomeOverdue, review.Outcome)

	reservation, dossier, reserved, err := ledger.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID,
		WorkerID: "cross-owner-conflict-reviewer", LocalAssessmentDate: reviewNow, Model: "cross-owner-test-model",
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, reservation)
	require.NotNil(t, dossier)
	confidence := 1.0
	_, err = ledger.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion: reservation.CaseVersion, ReviewRunID: run.ReviewRunID, Decision: "selected",
		SelectedPositionID: preferredPositionID, Confidence: &confidence, ProviderTurns: 1, ResponseHash: "cross-owner-test-response",
	})
	require.NoError(t, err)

	result := commitOverdueConflictResolutionWithVectors(t, ctx, ledger, ApplyOverdueConflictResolutionInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID,
		WorkerID: "cross-owner-conflict-reviewer", ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: preferredPositionID, AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method: "ai", Now: reviewNow,
	})
	require.True(t, result.Resolved)
	require.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)
	require.NotContains(t, result.RetractedEvidenceIDs, knownFragmentID)

	var knownLifecycleCount, knownSupportCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND target_fragment_id = ?::uuid
		`, teamID, knownFragmentID).Scan(&knownLifecycleCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			  AND decision = 'grant'
		`, teamID, unrelated.Relationship.RelationshipID).Scan(&knownSupportCount).Error
	}))
	require.Zero(t, knownLifecycleCount, "cross-owner known evidence must not be retracted")
	require.EqualValues(t, 2, knownSupportCount, "B's relationship keeps both submitted and known support grants")
}

func TestOverdueConflictResolutionRetainsEvidenceUsedByLosingOwner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-losing-owner-support", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-losing-owner-support-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-losing-owner-support-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-losing-owner-support-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-losing-owner-support-owner-c")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Losing-owner conflict subject")
	preferredObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Losing-owner preferred object")
	losingObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Losing-owner losing object")
	unrelatedSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Losing-owner unrelated subject")
	unrelatedObject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Losing-owner unrelated object")
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerB,
		"losing-owner-known-evidence", "Owner B's evidence is used by Owner A relationships.")
	knownContent := known.Evidence[0].Content
	knownSupport := EvidenceSupportInput{
		FragmentID: known.Evidence[0].FragmentID, EvidenceOwnerProfileID: ownerB,
		SourceGroupKey: "losing-owner-known-support", SpanStart: 0, SpanEnd: len([]rune(knownContent)),
		Quote: knownContent, Authority: "primary",
	}
	loser := commitConflictRememberFixtureWithSupports(t, ctx, ledger, teamID, ownerA, subject.EntityID, losingObject.EntityID,
		"Owner A's losing relationship uses Owner B's evidence.", "losing-owner-conflict-loser", []EvidenceSupportInput{knownSupport})
	require.NotEmpty(t, loser.RelationshipResults)
	require.NotNil(t, loser.RelationshipResults[0].Relationship)

	unrelatedSubmitted := createSemanticIngest(t, ctx, ledger, teamID, ownerA,
		"losing-owner-unrelated-submitted", "Owner A's unrelated relationship also uses Owner B's evidence.")
	unrelated := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerA, IngestID: unrelatedSubmitted.IngestID,
		SubjectEntityID: unrelatedSubject.EntityID, PredicateKey: "uses", ObjectEntityID: unrelatedObject.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support: &EvidenceSupportInput{
			FragmentID: unrelatedSubmitted.Evidence[0].FragmentID, SourceGroupKey: "losing-owner-unrelated-submitted",
			SpanStart: 0, SpanEnd: len([]rune(unrelatedSubmitted.Evidence[0].Content)), Authority: "primary",
		},
		Supports: []EvidenceSupportInput{knownSupport},
	})
	require.NotNil(t, unrelated.Relationship)

	preferred := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerC, subject.EntityID, preferredObject.EntityID,
		"Owner C's preferred relationship uses another object.", "losing-owner-conflict-preferred")
	require.NotEmpty(t, preferred.RelationshipResults)

	var conflictID string
	var dueAt time.Time
	var preferredPositionID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT conflict_id::text, review_due_at
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID).Row().Scan(&conflictID, &dueAt); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT position_id::text
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND object_entity_id = ?::uuid
		`, teamID, conflictID, preferredObject.EntityID).Row().Scan(&preferredPositionID)
	}))
	require.NotEmpty(t, conflictID)
	require.NotEmpty(t, preferredPositionID)
	reviewNow := dueAt.Add(time.Minute)
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID: teamID, WorkerID: "losing-owner-conflict-reviewer", LocalRunDate: reviewNow, Timezone: "UTC", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := ledger.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID: teamID, WorkerID: "losing-owner-conflict-reviewer", ReviewRunID: run.ReviewRunID,
		Limit: 10, Lease: time.Minute, MaxAttempts: 5, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	review, err := ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: teamID, WorkerID: "losing-owner-conflict-reviewer", ReviewRunID: run.ReviewRunID,
		ConflictID: conflictID, Now: reviewNow,
	})
	require.NoError(t, err)
	require.Equal(t, ConflictReviewOutcomeOverdue, review.Outcome)

	reservation, dossier, reserved, err := ledger.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID,
		WorkerID: "losing-owner-conflict-reviewer", LocalAssessmentDate: reviewNow, Model: "losing-owner-test-model",
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, reservation)
	require.NotNil(t, dossier)
	confidence := 1.0
	_, err = ledger.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion: reservation.CaseVersion, ReviewRunID: run.ReviewRunID, Decision: "selected",
		SelectedPositionID: preferredPositionID, Confidence: &confidence, ProviderTurns: 1, ResponseHash: "losing-owner-test-response",
	})
	require.NoError(t, err)

	result := commitOverdueConflictResolutionWithVectors(t, ctx, ledger, ApplyOverdueConflictResolutionInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: run.ReviewRunID,
		WorkerID: "losing-owner-conflict-reviewer", ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: preferredPositionID, AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method: "ai", Now: reviewNow,
	})
	require.True(t, result.Resolved)
	require.ElementsMatch(t, []string{loser.RelationshipResults[0].Relationship.RelationshipID}, result.UpdatedRelationships)
	require.NotContains(t, result.RetractedEvidenceIDs, known.Evidence[0].FragmentID)

	var knownLifecycleCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND target_fragment_id = ?::uuid
		`, teamID, known.Evidence[0].FragmentID).Scan(&knownLifecycleCount).Error
	}))
	require.Zero(t, knownLifecycleCount, "evidence used by the losing owner must not be retracted")
}

func commitConflictRememberFixture(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID, ownerID, subjectID, objectID, content, key string,
) *SynchronousRememberCommitResult {
	return commitConflictRememberFixtureWithSupports(t, ctx, repo, teamID, ownerID, subjectID, objectID, content, key, nil)
}

func commitConflictRememberFixtureWithSupports(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID, ownerID, subjectID, objectID, content, key string,
	supports []EvidenceSupportInput,
) *SynchronousRememberCommitResult {
	t.Helper()
	input := conflictRememberFixtureInput(teamID, ownerID, subjectID, objectID, content, key, supports)
	vectors := conflictRememberFixtureVectors(t, ctx, repo, input)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, vectors)
	require.NoError(t, err)
	return result
}

func conflictRememberFixtureInput(
	teamID, ownerID, subjectID, objectID, content, key string,
	supports []EvidenceSupportInput,
) SynchronousRememberCommitInput {
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	relationshipRef := key + ":relationship"
	return SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: key, RequestHash: sha256Hex(content), SourceSummary: key,
		Evidence: []EvidenceInput{{
			FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content),
			SourceType: "conversation", Authority: "primary",
		}},
		EvidenceSecurityResults: []EvidenceSecurityResult{{
			FragmentID: fragmentID, EvidenceIndex: 0, Decision: "pass", Safe: true,
		}},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + key + `"}`), ProviderTurns: 1,
		Commit: CommitSubmissionAssessmentInput{
			AssessmentID: assessmentID,
			Items:        []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
			EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
				{Resolution: SemanticEntityResolutionInput{MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subjectID, ExactEntityID: subjectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
				{Resolution: SemanticEntityResolutionInput{MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: objectID, ExactEntityID: objectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
			},
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
				RelationshipRef: relationshipRef,
				Observation: SemanticRelationshipDecisionInput{
					Ref: relationshipRef, SubjectRef: "subject", OriginalPredicate: "primary_database", PredicateKey: "primary_database", PredicateVersion: 1,
					ObjectRef: "object", Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID,
					Support:  &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: key, SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary"},
					Supports: supports,
				},
			}},
			RelationshipResults: []SubmissionRelationshipResultInput{{RelationshipRef: relationshipRef, Disposition: "stored"}},
			Payload:             map[string]any{"response_hash": sha256Hex(key), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
		},
	}
}

func conflictRememberFixtureVectors(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	input SynchronousRememberCommitInput,
) []InlineEmbeddingResult {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	vectors := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for index, document := range plan.Documents {
		vector := []float32{1, 0, 0}
		if index%2 == 1 {
			vector = []float32{0, 1, 0}
		}
		vectors = append(vectors, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
		})
	}
	return vectors
}
