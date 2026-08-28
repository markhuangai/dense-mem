package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

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

func TestSearchRepairCandidatePageSkipsHealthyCanonicalSourceBeforeHydration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	firstTeamID := createLedgerTeam(t, adminDB, rls, "search-repair-bounded-first-team")
	firstOwnerID := createLedgerProfile(t, adminDB, rls, firstTeamID, "search-repair-bounded-first-owner")
	secondTeamID := createLedgerTeam(t, adminDB, rls, "search-repair-bounded-second-team")
	secondOwnerID := createLedgerProfile(t, adminDB, rls, secondTeamID, "search-repair-bounded-second-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-bounded", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	firstIngest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: firstTeamID, OwnerProfileID: firstOwnerID, IdempotencyKey: "search-repair-bounded-first",
		RequestHash: sha256Hex("search repair bounded first"), Evidence: []EvidenceInput{{Content: "search repair bounded first"}},
	})
	firstDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: firstTeamID, OwnerProfileID: firstOwnerID, SourceKind: "evidence", SourceID: firstIngest.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "search repair bounded first",
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	secondIngest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: secondTeamID, OwnerProfileID: secondOwnerID, IdempotencyKey: "search-repair-bounded-second",
		RequestHash: sha256Hex("search repair bounded second"), Evidence: []EvidenceInput{{Content: "search repair bounded second"}},
	})
	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: secondTeamID, OwnerProfileID: secondOwnerID, SourceKind: "evidence", SourceID: secondIngest.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "search repair bounded second",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[1,0]'::vector,
			    embedding_updated_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, firstTeamID, firstDocument.SearchDocumentID).Error; err != nil {
			return err
		}
		return nil
	}))
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	teamLocked := make(chan struct{})
	releaseTeam := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			var lockedTeamID string
			if err := tx.Raw(`
				SELECT id::text
				FROM teams
				WHERE id = ?::uuid
				FOR UPDATE
			`, firstTeamID).Row().Scan(&lockedTeamID); err != nil {
				return err
			}
			close(teamLocked)
			<-releaseTeam
			return nil
		})
	}()
	<-teamLocked
	selectionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	selected, hasMore, err := repo.SelectSearchRepairDocuments(selectionCtx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: 2,
		Limit:               1,
	})
	cancel()
	close(releaseTeam)
	require.NoError(t, <-lockErr)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, secondIngest.Evidence[0].FragmentID, selected[0].SourceID)
	require.False(t, hasMore)
}
