package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchRepairActiveTeamFenceBlocksConcurrentSoftDelete(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-team-lock")
	locked := make(chan struct{})
	release := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			signalled := false
			defer func() {
				if !signalled {
					close(locked)
				}
			}()
			active, err := lockSearchRepairActiveTeam(ctx, tx, teamID)
			if err != nil {
				return err
			}
			if !active {
				return gorm.ErrRecordNotFound
			}
			signalled = true
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE teams SET status = 'deleted', deleted_at = clock_timestamp() WHERE id = ?::uuid`, teamID).Error
		})
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("soft delete completed while repair held its active-team fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-lockErr)
	require.NoError(t, <-deleteDone)
}

func TestSearchRepairApplyHoldsRunLeaseWhileWaitingForSource(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-run-lock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-run-lock-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-run-lock", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Run Lock Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Run Lock Object")
	content := "Run Lock Subject uses Run Lock Object."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "search-repair-run-lock-ingest", content)
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-run-lock",
			SpanStart: 0, SpanEnd: len(content), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: decision.Relationship.RelationshipID,
		SourceVersion: int64(decision.Relationship.Version), ProjectionFormat: 2, DocumentText: "stale run-lock relationship",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "run-lock-worker", Lease: time.Second,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)

	operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sourceReady := make(chan struct{})
	sourceRelease := make(chan struct{})
	sourceDone := make(chan error, 1)
	var blockerPID int
	go func() {
		sourceDone <- rls.WithSystemTx(operationCtx, adminDB, func(tx *gorm.DB) error {
			if err := tx.Raw(`SELECT pg_backend_pid()`).Row().Scan(&blockerPID); err != nil {
				return err
			}
			var locked string
			if err := tx.Raw(`
				SELECT relationship_id::text
				FROM relationship_records
				WHERE team_id = ?::uuid AND relationship_id = ?::uuid
				FOR UPDATE
			`, teamID, decision.Relationship.RelationshipID).Row().Scan(&locked); err != nil {
				return err
			}
			close(sourceReady)
			select {
			case <-sourceRelease:
			case <-operationCtx.Done():
				return operationCtx.Err()
			}
			return nil
		})
	}()
	select {
	case <-sourceReady:
	case <-operationCtx.Done():
		require.FailNow(t, "run-lock source blocker was not acquired", operationCtx.Err())
	}

	type applyOutcome struct {
		result *SearchRepairApplyResult
		err    error
	}
	applyDone := make(chan applyOutcome, 1)
	go func() {
		result, applyErr := repo.ApplySearchRepair(operationCtx, ApplySearchRepairInput{
			RunID: run.RunID, LeaseToken: run.LeaseToken,
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
			SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
			Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
		})
		applyDone <- applyOutcome{result: result, err: applyErr}
	}()
	requirePostgresBackendBlockedBy(t, operationCtx, adminDB, rls, blockerPID)
	time.Sleep(1200 * time.Millisecond)

	type reserveOutcome struct {
		run     *SearchRepairRun
		claimed bool
		err     error
	}
	reserveDone := make(chan reserveOutcome, 1)
	go func() {
		reserved, reserveClaimed, reserveErr := repo.ReserveSearchRepairRun(operationCtx, SearchRepairRunInput{
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
			LocalRunDate: run.LocalRunDate, WorkerID: "run-lock-reclaimer", Lease: time.Minute,
		})
		reserveDone <- reserveOutcome{run: reserved, claimed: reserveClaimed, err: reserveErr}
	}()
	select {
	case outcome := <-reserveDone:
		t.Fatalf("run was reclaimed while apply still held its lease fence: claimed=%v err=%v", outcome.claimed, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(sourceRelease)
	require.NoError(t, <-sourceDone)
	applyOutcomeResult := <-applyDone
	require.Error(t, applyOutcomeResult.err)
	require.Nil(t, applyOutcomeResult.result)
	reserveOutcomeResult := <-reserveDone
	require.NoError(t, reserveOutcomeResult.err)
	require.True(t, reserveOutcomeResult.claimed)
	require.NotNil(t, reserveOutcomeResult.run)
}
