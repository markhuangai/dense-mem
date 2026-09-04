package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRelationshipCorrectionLocksCrossScopePairCanonically(t *testing.T) {
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
	insertSearchTestContract(t, adminDB, rls, "correction-scope-pair-order", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "correction-scope-pair-order-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "correction-scope-pair-order-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subjectA := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Correction scope A")
	subjectB := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Correction scope B")
	objectA := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Correction object A")
	objectB := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Correction object B")
	first := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerID, subjectA.EntityID, objectA.EntityID, "Correction scope A uses object A.", "correction-scope-pair-a")
	second := commitConflictRememberFixture(t, ctx, ledger, teamID, ownerID, subjectB.EntityID, objectB.EntityID, "Correction scope B uses object B.", "correction-scope-pair-b")
	firstRelationship := first.RelationshipResults[0].Relationship
	secondRelationship := second.RelationshipResults[0].Relationship
	require.NotNil(t, firstRelationship)
	require.NotNil(t, secondRelationship)

	firstScope := relationshipConflictScopeKey(firstRelationship, firstRelationship.SpaceID, string(domain.MemorySpaceTeamShared))
	secondScope := relationshipConflictScopeKey(secondRelationship, secondRelationship.SpaceID, string(domain.MemorySpaceTeamShared))
	source := firstRelationship
	targetSubject := subjectB
	sourceScope, targetScope := firstScope, secondScope
	if sourceScope < targetScope {
		source = secondRelationship
		targetSubject = subjectA
		sourceScope, targetScope = secondScope, firstScope
	}
	require.Greater(t, sourceScope, targetScope)

	var evidenceID string
	var supportStart, supportEnd int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT fragment_id::text, span_start, span_end
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			ORDER BY created_at, support_id
			LIMIT 1
		`, teamID, source.RelationshipID).Row().Scan(&evidenceID, &supportStart, &supportEnd)
	}))

	correction := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: source.RelationshipID, ExpectedVersion: source.Version,
		Patch:    RelationshipCorrectionPatch{SubjectEntity: &RelationshipCorrectionEntityPatch{EntityID: targetSubject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: evidenceID, Start: supportStart, End: supportEnd}},
		Reason:   "the subject Entity was resolved incorrectly", IdempotencyKey: "correction-scope-pair-order",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, correction)
	require.NoError(t, err)

	scopeHeld := make(chan struct{})
	releaseScope := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			if err := lockRelationshipConflictSnapshotScope(ctx, tx, teamID, sourceScope); err != nil {
				return err
			}
			close(scopeHeld)
			select {
			case <-releaseScope:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-scopeHeld:
	case err := <-holderDone:
		require.NoError(t, err)
		t.Fatal("scope holder exited before acquiring the source scope")
	case <-ctx.Done():
		t.Fatal("timed out acquiring the source scope")
	}
	released := false
	defer func() {
		if !released {
			close(releaseScope)
			require.NoError(t, <-holderDone)
		}
	}()

	type correctionOutcome struct {
		result *CorrectRelationshipResult
		err    error
	}
	correctionDone := make(chan correctionOutcome, 1)
	go func() {
		result, correctionErr := semantic.CorrectRelationshipWithEmbeddings(ctx, correction, relationshipCorrectionTestEmbeddings(plan))
		correctionDone <- correctionOutcome{result: result, err: correctionErr}
	}()

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
		`, teamID+":relationship-conflict-snapshot:"+sourceScope).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case outcome := <-correctionDone:
			require.NoError(t, outcome.err, "correction returned before waiting for the source scope")
			t.Fatal("correction returned before waiting for the source scope")
		case <-waitCtx.Done():
			t.Fatal("correction did not wait for the source scope")
		case <-time.After(10 * time.Millisecond):
		}
	}

	var targetScopeHeld bool
	require.NoError(t, adminSQL.QueryRowContext(ctx, `
		WITH scope AS (
			SELECT hashtextextended($1::text, 0) AS advisory_key
		)
		SELECT EXISTS (
			SELECT 1
			FROM pg_locks, scope
			WHERE locktype = 'advisory'
			  AND granted
			  AND objsubid = 1
			  AND classid::bigint = ((scope.advisory_key >> 32) & 4294967295)
			  AND objid::bigint = (scope.advisory_key & 4294967295)
		)
	`, teamID+":relationship-conflict-snapshot:"+targetScope).Scan(&targetScopeHeld))
	require.True(t, targetScopeHeld, "correction must acquire the lower destination scope before waiting for the higher source scope")

	close(releaseScope)
	released = true
	require.NoError(t, <-holderDone)
	select {
	case outcome := <-correctionDone:
		require.NoError(t, outcome.err)
		require.Equal(t, "completed", outcome.result.ProcessingState)
	case <-ctx.Done():
		t.Fatal("correction did not complete after the source scope released")
	}
}
