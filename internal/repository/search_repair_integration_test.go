package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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
	content := "\tcanonical whitespace evidence\u00a0\n"
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

func TestSearchRepairExcludesQueuedRejectedRememberEvidenceAndRetiresLegacyDocument(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-rejected-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-rejected-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-rejected", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-rejected-evidence",
		RequestHash: sha256Hex("rejected search repair evidence"), Evidence: []EvidenceInput{{Content: "rejected search repair evidence"}},
	})
	require.NoError(t, err)
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "search-repair-rejected-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = ledger.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "search-repair-rejected-worker", ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "search_repair_rejected_fixture", Status: string(domain.SemanticReviewRejected), Category: "rejected",
		Payload: map[string]any{"failure_code": "no_supported_memory"},
	})
	require.NoError(t, err)
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
	require.Equal(t, "rejected", placementStatus)
	require.Equal(t, "rejected", itemStatus)
	legacy, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: ingest.Evidence[0].FragmentID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "rejected search repair evidence",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Zero(t, projection.ExpectedDocuments)
	require.EqualValues(t, 1, projection.DriftedDocuments)
	run, claimedRun, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "repair-rejected-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimedRun)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, legacy.SearchDocumentID, selected[0].SearchDocumentID)
	require.True(t, selected[0].Retired)
	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0]}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	var state string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, legacy.SearchDocumentID).Row().Scan(&state)
	}))
	require.Equal(t, "not_required", state)
}

func TestSearchRepairFindsDriftBeyondCurrentDocumentPrefix(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-continuation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-continuation-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-continuation", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	documents := make([]*SearchDocumentResult, 0, 5)
	for index := 0; index < 5; index++ {
		document, upsertErr := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: uuid.NewString(),
			SourceVersion: 1, DocumentText: "bounded continuation entity",
		})
		require.NoError(t, upsertErr)
		documents = append(documents, document)
	}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for index, document := range documents[:4] {
			if err := tx.Exec(`
				UPDATE search_documents
				SET search_state = 'current', embedding = '[1,0]'::vector,
				    embedding_updated_at = clock_timestamp(), updated_at = clock_timestamp() - (?::integer * interval '1 minute')
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, 10-index, teamID, document.SearchDocumentID).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`
			UPDATE search_documents SET updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, documents[4].SearchDocumentID).Error
	}))
	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, documents[4].SearchDocumentID, selected[0].SearchDocumentID)
	require.False(t, hasMore)
}

func TestSearchRepairRefillsAfterHydrationExclusions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-refill-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-refill-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-refill", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	contents := []string{
		"search repair refill evidence one",
		"search repair refill evidence two",
		"search repair refill evidence three",
		"search repair refill evidence four",
		"search repair refill evidence five",
	}
	var targetSourceID string
	for index, content := range contents {
		ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-refill-evidence-" + content,
			RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
		})
		fragmentID := ingest.Evidence[0].FragmentID
		if index == len(contents)-1 {
			targetSourceID = fragmentID
			continue
		}
		document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragmentID,
			SourceVersion: 1, DocumentText: content,
		})
		require.NoError(t, err)
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE search_documents
				SET search_state = 'current', embedding = '[1,0]'::vector,
				    embedding_updated_at = clock_timestamp(), updated_at = clock_timestamp()
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, teamID, document.SearchDocumentID).Error
		}))
	}
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, selected, 1)
	require.Equal(t, targetSourceID, selected[0].SourceID)
}

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
			active, err := lockSearchRepairActiveTeam(ctx, tx, teamID)
			if err != nil {
				return err
			}
			if !active {
				return gorm.ErrRecordNotFound
			}
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
