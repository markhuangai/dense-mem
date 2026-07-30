package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementSemanticCommitWaitsForEvidenceLifecycleRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-lifecycle-lock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-lifecycle-lock-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Riley")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	content := "Riley works on Dense-Mem."
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "placement lifecycle lock", content)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-lifecycle-lock", time.Minute)
	require.NoError(t, err)

	releaseLifecycle := make(chan struct{})
	closeLifecycle := func() {
		select {
		case <-releaseLifecycle:
		default:
			close(releaseLifecycle)
		}
	}
	t.Cleanup(closeLifecycle)
	lifecycleLocked := make(chan error, 1)
	lifecycleDone := make(chan error, 1)
	operation := evidenceLifecycleOperationInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Action:         "retract",
		EvidenceIDs:    []string{ingest.Evidence[0].FragmentID},
		Reason:         "retracted while placement was pending",
		IdempotencyKey: "placement-lifecycle-lock-retract",
		RequestHash:    "sha256:placement-lifecycle-lock-retract",
	}
	go func() {
		lifecycleDone <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			planned, err := planEvidenceLifecycle(ctx, tx, operation)
			lifecycleLocked <- err
			if err != nil {
				return err
			}
			<-releaseLifecycle
			operationID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, planned)
			if err != nil {
				return err
			}
			if err := insertEvidenceLifecycleEvents(ctx, tx, operation, operationID); err != nil {
				return err
			}
			return applyEvidenceLifecycleEffects(ctx, tx, operation, operationID, planned)
		})
	}()
	select {
	case err := <-lifecycleLocked:
		require.NoError(t, err)
	case err := <-lifecycleDone:
		require.NoError(t, err)
		t.Fatal("lifecycle transaction ended before acquiring the target lock")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle target lock")
	}

	type placementCommitOutcome struct {
		result *CommitPlacementSemanticResult
		err    error
	}
	placementDone := make(chan placementCommitOutcome, 1)
	go func() {
		result, err := ledgerRepo.CommitPlacementSemanticResult(ctx, CommitPlacementSemanticInput{
			TeamID:           teamID,
			OwnerProfileID:   ownerID,
			IngestID:         ingest.IngestID,
			PlacementRunID:   ingest.PlacementRunID,
			PlacementItemID:  ingest.Items[0].PlacementItemID,
			WorkerID:         "worker-lifecycle-lock",
			ExpectedAttempts: claimed.Attempts,
			RelationshipDecisions: []ApplyRelationshipDecisionInput{{
				SubjectEntityID: subject.EntityID,
				PredicateKey:    "works_on",
				ObjectEntityID:  object.EntityID,
				Support: &EvidenceSupportInput{
					FragmentID:     ingest.Evidence[0].FragmentID,
					SourceGroupKey: "source:placement-lifecycle-lock",
					SpanStart:      0,
					SpanEnd:        len(content),
					Quote:          content,
					Authority:      "primary",
				},
			}},
		})
		placementDone <- placementCommitOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-placementDone:
		closeLifecycle()
		require.NoError(t, <-lifecycleDone)
		require.NoError(t, outcome.err)
		t.Fatalf("placement completed before lifecycle retraction committed with status %q", outcome.result.Status)
	case <-time.After(250 * time.Millisecond):
	}

	closeLifecycle()
	select {
	case err := <-lifecycleDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle retraction to commit")
	}
	select {
	case outcome := <-placementDone:
		require.NoError(t, outcome.err)
		require.Equal(t, "superseded", outcome.result.Status)
		require.NotEmpty(t, outcome.result.OutcomeID)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for placement commit")
	}

	var relationshipCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&relationshipCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), relationshipCount)
}
