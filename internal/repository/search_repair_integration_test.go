package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createSearchRepairAcceptedIngest(
	t *testing.T,
	ctx context.Context,
	ledger *LedgerRepositoryImpl,
	input CreateIngestInput,
) *CreateIngestResult {
	t.Helper()
	ingest, err := ledger.CreateIngest(ctx, input)
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	require.Len(t, ingest.Items, 1)
	workerID := "search-repair-fixture-" + input.IdempotencyKey
	claimed, err := ledger.ClaimNextPlacementRun(ctx, input.TeamID, workerID, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = commitAcceptedSubmissionFixture(t, ctx, ledger, CommitPlacementSemanticInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
		IngestID: ingest.IngestID, PlacementRunID: ingest.PlacementRunID,
		PlacementItemID: ingest.Items[0].PlacementItemID,
		WorkerID:        workerID, ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)
	return ingest
}

func rotateSearchRepairIndexGenerationForTest(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	contract *ActiveSearchContract,
) string {
	t.Helper()
	nextID := uuid.NewString()
	var nextGeneration int
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT generation
			FROM search_index_generations
			WHERE search_index_generation_id = ?::uuid
			FOR UPDATE
		`, contract.SearchIndexGenerationID).Row().Scan(&nextGeneration); err != nil {
			return err
		}
		nextGeneration++
		if err := tx.Exec(`
			UPDATE search_index_generations
			SET activation_state = 'deprecated'
			WHERE embedding_contract_id = ?::uuid
			  AND activation_state = 'active'
		`, contract.EmbeddingContractID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (
				search_index_generation_id, generation, embedding_contract_id,
				embedding_dimensions, ann_strategy, operator_class,
				indexed_expression, physical_index_name, exact_max_rows,
				allow_exact_fallback, activation_state, activated_at
			)
			SELECT ?::uuid, ?, embedding_contract_id,
			       embedding_dimensions, ann_strategy, operator_class,
			       indexed_expression, physical_index_name, exact_max_rows,
			       allow_exact_fallback, 'active', clock_timestamp()
			FROM search_index_generations
			WHERE search_index_generation_id = ?::uuid
		`, nextID, nextGeneration, contract.SearchIndexGenerationID).Error
	}))
	return nextID
}

func TestSearchRepairSelectsQueuedRememberEvidenceAfterCompletedPlacement(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-canonical-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-canonical-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-canonical", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-canonical-evidence",
		RequestHash: sha256Hex("canonical search repair evidence"), Evidence: []EvidenceInput{{Content: "canonical search repair evidence"}},
	})
	var ingestStatus, placementStatus, itemStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT ingest.status, run.status, item.status
			FROM knowledge_ingests AS ingest
			JOIN placement_runs AS run
			  ON run.team_id = ingest.team_id AND run.ingest_id = ingest.ingest_id
			JOIN placement_items AS item
			  ON item.team_id = run.team_id AND item.placement_run_id = run.placement_run_id
			WHERE ingest.team_id = ?::uuid AND ingest.ingest_id = ?::uuid
		`, teamID, ingest.IngestID).Row().Scan(&ingestStatus, &placementStatus, &itemStatus)
	}))
	require.Equal(t, "queued", ingestStatus)
	require.Equal(t, "completed", placementStatus)
	require.Equal(t, "completed", itemStatus)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	before, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, before.ExpectedDocuments)
	require.EqualValues(t, 1, before.DriftedDocuments)
	require.Contains(t, before.DriftClasses, SearchDocumentDriftCount{Class: "document_fence_or_vector", Count: 1})

	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	_, err = repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: uuid.NewString(),
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})
	require.Error(t, err)
	_, err = repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: uuid.NewString(), IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)

	after, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, after.CurrentDocuments)
	require.Zero(t, after.DriftedDocuments)
}

func TestSearchRepairConvergesWhitespaceDelimitedCanonicalEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-whitespace-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-whitespace-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-whitespace", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	content := "\u0085\u2007\tcanonical whitespace evidence\u00a0\u202f\n"
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-whitespace-evidence",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-whitespace-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	require.Equal(t, "canonical whitespace evidence", selected[0].DocumentText)
	_, err = repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Zero(t, projection.DriftedDocuments)
}

func TestSearchRepairUpdatesExistingDocumentUsingStoredFenceValues(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-stored-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-stored-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-stored-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	content := "canonical stored fence evidence"
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-stored-fence-evidence",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
	})
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: ingest.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale stored fence evidence",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "search-repair-stored-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, selected, 1)
	require.Equal(t, document.SearchDocumentID, selected[0].SearchDocumentID)
	require.Equal(t, content, selected[0].DocumentText)

	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.Zero(t, apply.RemainingDriftedCount)

	var repairedText string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT document_text
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Row().Scan(&repairedText)
	}))
	require.Equal(t, content, repairedText)
}

func TestSearchRepairRepairsCanonicalEvidenceOwnerMismatch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-owner-mismatch-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-owner-mismatch-owner")
	staleOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-owner-mismatch-stale-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-owner-mismatch", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-owner-mismatch-evidence",
		RequestHash: sha256Hex("owner mismatch repair evidence"), Evidence: []EvidenceInput{{Content: "owner mismatch repair evidence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	firstRun, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "owner-mismatch-seed-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	missing, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, missing, 1)
	require.Equal(t, ownerID, missing[0].OwnerProfileID)
	_, err = repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: firstRun.RunID, LeaseToken: firstRun.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: missing[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.NoError(t, repo.FinishSearchRepairRun(ctx, FinishSearchRepairRunInput{
		RunID: firstRun.RunID, LeaseToken: firstRun.LeaseToken, Status: "completed",
		SelectedCount: 1, EmbeddedCount: 1, UpdatedCount: 1,
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET owner_profile_id = ?::uuid, updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, staleOwnerID, teamID, missing[0].SearchDocumentID).Error
	}))
	secondRun, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC().Add(24 * time.Hour), CreateIfMissing: true, WorkerID: "owner-mismatch-repair-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, selected, 1)
	require.Equal(t, ownerID, selected[0].OwnerProfileID)
	require.Equal(t, staleOwnerID, selected[0].StoredOwnerProfileID)
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: secondRun.RunID, LeaseToken: secondRun.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	var repairedOwner string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT owner_profile_id::text
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, missing[0].SearchDocumentID).Row().Scan(&repairedOwner)
	}))
	require.Equal(t, ownerID, repairedOwner)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Zero(t, convergence.DriftedDocuments)
}

func TestSearchRepairDoesNotCommitAfterRelationshipRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-relationship-source-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-relationship-source-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-relationship-source-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Source Fence Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Source Fence Object")
	content := "Source Fence Subject uses Source Fence Object."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "search-repair-relationship-source-fence-ingest", content)
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-relationship-source-fence",
			SpanStart: 0, SpanEnd: len(content), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: decision.Relationship.RelationshipID,
		SourceVersion: int64(decision.Relationship.Version), ProjectionFormat: 2, DocumentText: "stale relationship source",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "relationship-source-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: searchRepairCandidateLimit,
	})
	require.NoError(t, err)
	var candidate SearchRepairDocument
	for _, item := range selected {
		if item.SourceKind == "relationship" && item.SourceID == decision.Relationship.RelationshipID {
			candidate = item
			break
		}
	}
	require.NotEmpty(t, candidate.SearchDocumentID)

	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
			return tx.Exec(`
				UPDATE relationship_records
				SET status = 'retracted', version = version + 1, updated_at = now()
				WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND owner_profile_id = ?::uuid
			`, teamID, decision.Relationship.RelationshipID, ownerID).Error
		})
	}()
	select {
	case <-sourceReady:
	case <-operationCtx.Done():
		require.FailNow(t, "relationship source lock was not acquired", operationCtx.Err())
	}

	applyDone := make(chan struct {
		result *SearchRepairApplyResult
		err    error
	}, 1)
	go func() {
		result, applyErr := repo.ApplySearchRepair(operationCtx, ApplySearchRepairInput{
			RunID: run.RunID, LeaseToken: run.LeaseToken,
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
			SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
			Documents: []SearchRepairEmbedding{{SearchRepairDocument: candidate, Embedding: []float32{0.25, 0.75}}},
		})
		applyDone <- struct {
			result *SearchRepairApplyResult
			err    error
		}{result: result, err: applyErr}
	}()
	requirePostgresBackendBlockedBy(t, operationCtx, adminDB, rls, blockerPID)
	close(sourceRelease)
	require.NoError(t, <-sourceDone)
	applyResult := <-applyDone
	require.NoError(t, applyResult.err)
	require.Zero(t, applyResult.result.UpdatedCount)
	require.EqualValues(t, 1, applyResult.result.SkippedCount)

	var status string
	var sourceVersion int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT relationship.status, relationship.version
			FROM relationship_records AS relationship
			WHERE relationship.team_id = ?::uuid AND relationship.relationship_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Row().Scan(&status, &sourceVersion)
	}))
	require.Equal(t, "retracted", status)
	require.Equal(t, decision.Relationship.Version+1, sourceVersion)
	var documentState string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT search_state
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, candidate.SearchDocumentID).Row().Scan(&documentState)
	}))
	require.NotEqual(t, "current", documentState)
}

func TestSearchRepairDoesNotCommitAfterEvidenceQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-evidence-source-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-evidence-source-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-evidence-source-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-evidence-source-fence",
		RequestHash: sha256Hex("evidence source fence"), Evidence: []EvidenceInput{{Content: "evidence source fence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "evidence-source-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	require.NotEmpty(t, selected[0].SearchDocumentID)
	initialState := selected[0].SearchState
	initialVersion := selected[0].DocumentVersion
	initialHash := selected[0].DocumentHash

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_quarantines (team_id, fragment_id, ingest_id, owner_profile_id, status, reason)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'active', 'source fence test')
		`, teamID, ingest.Evidence[0].FragmentID, ingest.IngestID, ownerID).Error
	}))
	applyResult, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})
	require.NoError(t, err)
	require.Zero(t, applyResult.UpdatedCount)
	require.EqualValues(t, 1, applyResult.SkippedCount)

	var quarantineStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM evidence_quarantines
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, ingest.Evidence[0].FragmentID).Row().Scan(&quarantineStatus)
	}))
	require.Equal(t, "active", quarantineStatus)
	var documentState, documentHash string
	var documentVersion int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT search_state, document_version, document_hash
			FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id = ?::uuid
		`, teamID, ingest.Evidence[0].FragmentID).Row().Scan(&documentState, &documentVersion, &documentHash)
	}))
	require.Equal(t, initialState, documentState)
	require.Equal(t, initialVersion, documentVersion)
	require.Equal(t, initialHash, documentHash)
}

func TestSearchRepairDoesNotCommitAfterTeamBecomesInactive(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-inactive-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-inactive-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-inactive", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-inactive-evidence",
		RequestHash: sha256Hex("inactive team repair evidence"), Evidence: []EvidenceInput{{Content: "inactive team repair evidence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-inactive-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE teams SET status = 'deleted', deleted_at = clock_timestamp() WHERE id = ?::uuid`, teamID).Error
	}))
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.Zero(t, apply.UpdatedCount)
	require.EqualValues(t, 1, apply.SkippedCount)
}

func TestSearchRepairExcludesQuarantinedEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-quarantined-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-quarantined-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-quarantined", 2, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-quarantined-evidence",
		RequestHash: sha256Hex("quarantined search repair evidence"),
		Evidence:    []EvidenceInput{{Content: "quarantined search repair evidence"}},
	})
	require.Len(t, ingest.Evidence, 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_quarantines (team_id, fragment_id, ingest_id, owner_profile_id, status, reason)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'active', 'test quarantine')
		`, teamID, ingest.Evidence[0].FragmentID, ingest.IngestID, ownerID).Error
	}))
	contract, err := NewSearchRepository(appDB, rls).GetActiveSearchContract(ctx)
	require.NoError(t, err)
	selected, _, err := NewSearchRepository(appDB, rls).SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	for _, item := range selected {
		require.NotEqual(t, ingest.Evidence[0].FragmentID, item.SourceID)
	}
}

func TestSearchRepairExcludesSealedSpaceDocument(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-sealed-space-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-sealed-space-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-sealed-space", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.New())
	require.NoError(t, err)
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "sealed repair document", SpaceID: privateSpace.ID.String(),
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	for _, item := range selected {
		require.NotEqual(t, document.SearchDocumentID, item.SearchDocumentID)
	}
}

func TestSearchRepairRunReservationHasOneWinnerAcrossInstances(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-reservation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-reservation-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-reservation", 2, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	_ = createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-reservation-evidence",
		RequestHash: sha256Hex("reservation repair evidence"), Evidence: []EvidenceInput{{Content: "reservation repair evidence"}},
	})
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	input := SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, Lease: time.Minute,
	}
	claims := make(chan bool, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, workerID := range []string{"repair-instance-a", "repair-instance-b"} {
		wait.Add(1)
		go func(worker string) {
			defer wait.Done()
			<-start
			candidate := input
			candidate.WorkerID = worker
			_, claimed, reserveErr := repo.ReserveSearchRepairRun(ctx, candidate)
			errs <- reserveErr
			claims <- claimed
		}(workerID)
	}
	close(start)
	wait.Wait()
	close(claims)
	close(errs)
	for reserveErr := range errs {
		require.NoError(t, reserveErr)
	}
	claimedCount := 0
	for claimed := range claims {
		if claimed {
			claimedCount++
		}
	}
	require.Equal(t, 1, claimedCount)
}

func TestSearchRepairRejectsSourceChangedAfterSelection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-stale-source-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-stale-source-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-stale-source", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Stale Source Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Stale Source Object")
	firstIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-stale-source-first", "Stale Source Subject works on Stale Source Object.")
	first := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "search-repair-stale-source-first",
			SpanStart:      0,
			SpanEnd:        len("Stale Source Subject works on Stale Source Object."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: first.Relationship.RelationshipID, SourceVersion: int64(first.Relationship.Version),
		DocumentText: "relationship\nsubject: Stale Source Subject\npredicate: works on\nobject: Stale Source Object\npolarity: positive",
	})
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-stale-source-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	secondIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-stale-source-second", "Stale Source Subject works on Stale Source Object again.")
	second := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "search-repair-stale-source-second",
			SpanStart:      0,
			SpanEnd:        len("Stale Source Subject works on Stale Source Object again."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, second.Relationship)
	require.Greater(t, second.Relationship.Version, first.Relationship.Version)
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.Zero(t, apply.UpdatedCount)
	require.EqualValues(t, 1, apply.SkippedCount)
}

func TestSearchRepairRejectsStoredHashChangedAfterSelection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-stale-hash-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-stale-hash-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-stale-hash", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-stale-hash-evidence",
		RequestHash: sha256Hex("stale hash repair evidence"), Evidence: []EvidenceInput{{Content: "stale hash repair evidence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-stale-hash-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	tamperedText := "tampered repair document"
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET document_text = ?, document_hash = ?, updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, tamperedText, searchRepairHash(tamperedText), teamID, selected[0].SearchDocumentID).Error
	}))
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.Zero(t, apply.UpdatedCount)
	require.EqualValues(t, 1, apply.SkippedCount)
}

func TestSearchRepairFinalMutationRejectsAnActivatedIndexGenerationChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-index-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-index-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-index-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-index-fence-evidence",
		RequestHash: sha256Hex("index generation fence evidence"), Evidence: []EvidenceInput{{Content: "index generation fence evidence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-index-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	nextGenerationID := rotateSearchRepairIndexGenerationForTest(t, ctx, adminDB, rls, contract)
	active, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	require.Equal(t, nextGenerationID, active.SearchIndexGenerationID)

	_, err = repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	var updated, skipped bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var applyErr error
		updated, skipped, applyErr = applySearchRepairDocument(ctx, tx, contract, SearchRepairEmbedding{
			SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5},
		})
		return applyErr
	}))
	require.False(t, updated)
	require.True(t, skipped)
	var state string
	var vectorPresent bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT search_state, embedding IS NOT NULL
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, selected[0].SearchDocumentID).Row().Scan(&state, &vectorPresent)
	}))
	require.Equal(t, "pending", state)
	require.False(t, vectorPresent)
}

func TestSearchRepairSkipsEvidenceSealedAfterSelection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-space-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-space-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-space-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	space, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, space.ID).Row().Scan(&generation)
	}))
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: generation,
		IdempotencyKey: "search-repair-space-fence-evidence", RequestHash: sha256Hex("space generation fence evidence"),
		Evidence: []EvidenceInput{{Content: "space generation fence evidence"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-space-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE id = ?::uuid AND team_id = ?::uuid
		`, space.ID, teamID).Error
	}))
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.Zero(t, apply.UpdatedCount)
	require.EqualValues(t, 1, apply.SkippedCount)
}

func TestSearchRepairSkipsRelationshipProjectionGenerationChangedAfterSelection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-projection-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-projection-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-projection-fence", 2, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	repo := NewSearchRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Projection Fence Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Projection Fence Object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "search-repair-projection-fence-ingest", "Projection Fence Subject uses Projection Fence Object.")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-projection-fence",
			SpanStart: 0, SpanEnd: len("Projection Fence Subject uses Projection Fence Object."), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	firstGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, firstGenerationID, 1, "current")
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-projection-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, decision.Relationship.RelationshipID, selected[0].SourceID)
	require.Equal(t, firstGenerationID, selected[0].ProjectionGenerationID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_projection_generations
			SET state = 'embedding', activated_at = NULL, updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid
		`, teamID, firstGenerationID).Error
	}))
	secondGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, secondGenerationID, 2, "current")
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.Zero(t, apply.UpdatedCount)
	require.EqualValues(t, 1, apply.SkippedCount)
	var staleDocuments int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			  AND projection_generation_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID, firstGenerationID).Scan(&staleDocuments).Error
	}))
	require.Zero(t, staleDocuments)
}
