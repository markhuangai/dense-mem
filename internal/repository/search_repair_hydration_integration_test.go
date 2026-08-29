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

func TestSearchRepairSelectionPersistsCursorForNextBoundedRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	first := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-cursor-first",
		RequestHash: sha256Hex("search repair cursor first"), Evidence: []EvidenceInput{{Content: "search repair cursor first"}},
	})
	time.Sleep(10 * time.Millisecond)
	second := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-cursor-second",
		RequestHash: sha256Hex("search repair cursor second"), Evidence: []EvidenceInput{{Content: "search repair cursor second"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runDate := time.Now().UTC()
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, CreateIfMissing: true, WorkerID: "search-repair-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Len(t, selected, 1)
	require.Equal(t, first.Evidence[0].FragmentID, selected[0].SourceID)

	resumed, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, WorkerID: "search-repair-cursor-observer", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.NotNil(t, resumed.SelectionCursor)
	require.Equal(t, first.Evidence[0].FragmentID, resumed.SelectionCursor.SourceID)
	continued, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, Cursor: resumed.SelectionCursor,
	})
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, continued, 1)
	require.Equal(t, second.Evidence[0].FragmentID, continued[0].SourceID)
	require.NoError(t, repo.FinishSearchRepairRun(ctx, FinishSearchRepairRunInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken, Status: "completed",
		SelectedCount: 1, DriftedCount: 1,
	}))
	next, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate.Add(24 * time.Hour), CreateIfMissing: true, WorkerID: "search-repair-cursor-next-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, next.SelectionCursor)
	require.Equal(t, first.Evidence[0].FragmentID, next.SelectionCursor.SourceID)
}

func TestSearchRepairSelectionRevisitsRelationshipChangedBehindCursor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-relationship-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-relationship-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-relationship-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Cursor Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Cursor Object")

	firstIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-relationship-cursor-first", "Cursor Subject works on Cursor Object.")
	first := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: firstIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: firstIngest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-relationship-cursor-first",
			SpanStart: 0, SpanEnd: len("Cursor Subject works on Cursor Object."), Authority: "primary",
		},
	})
	secondIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-relationship-cursor-second", "Cursor Subject uses Cursor Object.")
	second := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: secondIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: secondIngest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-relationship-cursor-second",
			SpanStart: 0, SpanEnd: len("Cursor Subject uses Cursor Object."), Authority: "primary",
		},
	})
	require.NotNil(t, first.Relationship)
	require.NotNil(t, second.Relationship)
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: first.Relationship.RelationshipID, SourceVersion: int64(first.Relationship.Version),
		ProjectionFormat: 2, DocumentText: "stale first relationship",
	})
	require.NoError(t, err)
	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: second.Relationship.RelationshipID, SourceVersion: int64(second.Relationship.Version),
		ProjectionFormat: 2, DocumentText: "stale second relationship",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET updated_at = CASE relationship_id
				WHEN ?::uuid THEN clock_timestamp() - interval '2 minutes'
				WHEN ?::uuid THEN clock_timestamp() - interval '1 minute'
			END
			WHERE team_id = ?::uuid AND relationship_id IN (?::uuid, ?::uuid)
		`, first.Relationship.RelationshipID, second.Relationship.RelationshipID, teamID,
			first.Relationship.RelationshipID, second.Relationship.RelationshipID).Error
	}))
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runDate := time.Now().UTC()
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, CreateIfMissing: true, WorkerID: "search-repair-relationship-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Len(t, selected, 1)
	require.Equal(t, first.Relationship.RelationshipID, selected[0].SourceID)

	resumed, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, WorkerID: "search-repair-relationship-cursor-observer", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.NotNil(t, resumed.SelectionCursor)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1, updated_at = ?
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, resumed.SelectionCursor.ObservedAt.Add(time.Microsecond), teamID, first.Relationship.RelationshipID).Error
	}))
	continued, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, Cursor: resumed.SelectionCursor,
	})
	require.NoError(t, err)
	require.Len(t, continued, 1)
	require.Equal(t, first.Relationship.RelationshipID, continued[0].SourceID)
}

func TestSearchRepairSelectionRevisitsRelationshipAfterProjectionGenerationChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-generation-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-generation-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-generation-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Generation Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Generation Object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-generation-cursor", "Generation Subject uses Generation Object.")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-generation-cursor",
			SpanStart: 0, SpanEnd: len("Generation Subject uses Generation Object."), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	firstGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, firstGenerationID, 1, "current")
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: decision.Relationship.RelationshipID, SourceVersion: int64(decision.Relationship.Version),
		ProjectionGenerationID: firstGenerationID, ProjectionFormat: 2, DocumentText: "stale generation projection",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runDate := time.Now().UTC()
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, CreateIfMissing: true, WorkerID: "search-repair-generation-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, decision.Relationship.RelationshipID, selected[0].SourceID)

	cursor := searchRepairCursorToInput(searchRepairCursorFrom(selected[0]))
	require.NotNil(t, cursor)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_projection_generations
			SET state = 'embedding', activated_at = NULL, updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid
		`, teamID, firstGenerationID).Error
	}))
	secondGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, secondGenerationID, 2, "current")
	continued, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, Cursor: cursor,
	})
	require.NoError(t, err)
	require.Len(t, continued, 1)
	require.Equal(t, decision.Relationship.RelationshipID, continued[0].SourceID)
	require.Equal(t, secondGenerationID, continued[0].ProjectionGenerationID)
}

func TestSearchRepairAcceptsRelationshipForegroundMetadata(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-foreground-metadata-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-foreground-metadata-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-foreground-metadata", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Metadata Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Metadata Object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-repair-foreground-metadata", "Metadata Subject uses Metadata Object.")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-foreground-metadata",
			SpanStart: 0, SpanEnd: len("Metadata Subject uses Metadata Object."), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	generationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, generationID, 1, "current")
	var document *SearchDocumentResult
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		var err error
		document, err = upsertPlacementRelationshipSearchDocument(ctx, tx, CommitPlacementSemanticInput{
			TeamID: teamID, OwnerProfileID: ownerID,
		}, decision.Relationship, 20)
		return err
	}))
	require.NotNil(t, document)
	require.Empty(t, document.ProjectionGenerationID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[0.5,0.5]'::vector,
			    embedding_updated_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	count, err := repo.CountSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
	})
	require.NoError(t, err)
	require.Zero(t, count)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, convergence.ExpectedDocuments)
	require.EqualValues(t, 1, convergence.CurrentDocuments)
	require.Zero(t, convergence.DriftedDocuments)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "search-repair-foreground-metadata-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Empty(t, selected)
}

func TestSearchRepairSelectionRevisitsDocumentDriftBehindCursor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-document-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-document-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-document-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	first := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-document-cursor-first",
		RequestHash: sha256Hex("search repair document cursor first"), Evidence: []EvidenceInput{{
			Content: "search repair document cursor first", SourceType: "document", SourceKey: "search-repair-document-cursor-first",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-document-cursor-first",
		}},
	})
	time.Sleep(10 * time.Millisecond)
	second := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-document-cursor-second",
		RequestHash: sha256Hex("search repair document cursor second"), Evidence: []EvidenceInput{{
			Content: "search repair document cursor second", SourceType: "document", SourceKey: "search-repair-document-cursor-second",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-document-cursor-second",
		}},
	})
	time.Sleep(10 * time.Millisecond)
	third := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-document-cursor-third",
		RequestHash: sha256Hex("search repair document cursor third"), Evidence: []EvidenceInput{{
			Content: "search repair document cursor third", SourceType: "document", SourceKey: "search-repair-document-cursor-third",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-document-cursor-third",
		}},
	})
	firstDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: first.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale first evidence",
	})
	require.NoError(t, err)
	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: second.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale second evidence",
	})
	require.NoError(t, err)
	thirdDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: third.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale third evidence",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "search-repair-document-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 2, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, selected, 2)
	require.Equal(t, first.Evidence[0].FragmentID, selected[0].SourceID)
	require.Equal(t, second.Evidence[0].FragmentID, selected[1].SourceID)
	resumed, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: run.LocalRunDate, WorkerID: "search-repair-document-cursor-observer", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.NotNil(t, resumed.SelectionCursor)
	cursor := resumed.SelectionCursor.ObservedAt
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', updated_at = ?::timestamptz
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, cursor.Add(time.Hour), teamID, firstDocument.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[0.5,0.5]'::vector,
			    embedding_updated_at = ?::timestamptz, updated_at = ?::timestamptz
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, cursor.Add(time.Minute), cursor.Add(time.Minute), teamID, thirdDocument.SearchDocumentID).Error
	}))
	continued, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, Cursor: resumed.SelectionCursor,
	})
	require.NoError(t, err)
	require.Len(t, continued, 1)
	require.Equal(t, first.Evidence[0].FragmentID, continued[0].SourceID)
}

func TestSearchRepairSelectionRevisitsEvidenceChangedBehindCursor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-evidence-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-evidence-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-evidence-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	first := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-evidence-cursor-first",
		RequestHash: sha256Hex("search repair evidence cursor first"), Evidence: []EvidenceInput{{
			Content: "search repair evidence cursor first", SourceType: "document", SourceKey: "search-repair-evidence-cursor-first",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-evidence-cursor-first",
		}},
	})
	time.Sleep(10 * time.Millisecond)
	second := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-evidence-cursor-second",
		RequestHash: sha256Hex("search repair evidence cursor second"), Evidence: []EvidenceInput{{
			Content: "search repair evidence cursor second", SourceType: "document", SourceKey: "search-repair-evidence-cursor-second",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-evidence-cursor-second",
		}},
	})
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: first.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale first evidence",
	})
	require.NoError(t, err)
	secondDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: second.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale second evidence",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runDate := time.Now().UTC()
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, CreateIfMissing: true, WorkerID: "search-repair-evidence-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, first.Evidence[0].FragmentID, selected[0].SourceID)

	resumed, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, WorkerID: "search-repair-evidence-cursor-observer", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.NotNil(t, resumed.SelectionCursor)
	cursor := resumed.SelectionCursor.ObservedAt
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE evidence_sources AS source
			SET updated_at = CASE source.source_id
				WHEN (SELECT source_id FROM evidence_fragments WHERE team_id = ?::uuid AND fragment_id = ?::uuid) THEN ?::timestamptz
				WHEN (SELECT source_id FROM evidence_fragments WHERE team_id = ?::uuid AND fragment_id = ?::uuid) THEN ?::timestamptz
				ELSE source.updated_at
			END
			WHERE source.team_id = ?::uuid
		`, teamID, first.Evidence[0].FragmentID, cursor.Add(time.Hour), teamID, second.Evidence[0].FragmentID,
			cursor.Add(-time.Hour), teamID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET updated_at = ?::timestamptz
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, cursor.Add(-2*time.Hour), teamID, secondDocument.SearchDocumentID).Error
	}))
	continued, _, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, Cursor: resumed.SelectionCursor,
	})
	require.NoError(t, err)
	require.Len(t, continued, 1)
	require.Equal(t, first.Evidence[0].FragmentID, continued[0].SourceID)
}

func TestSearchRepairReclaimClearsPersistedSelectionCursor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-reclaim-cursor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-reclaim-cursor-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-reclaim-cursor", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	first := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-reclaim-cursor-first",
		RequestHash: sha256Hex("search repair reclaim cursor first"), Evidence: []EvidenceInput{{Content: "search repair reclaim cursor first"}},
	})
	time.Sleep(10 * time.Millisecond)
	_ = createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-reclaim-cursor-second",
		RequestHash: sha256Hex("search repair reclaim cursor second"), Evidence: []EvidenceInput{{Content: "search repair reclaim cursor second"}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runDate := time.Now().UTC()
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, CreateIfMissing: true, WorkerID: "search-repair-reclaim-cursor-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit: 1, RunID: run.RunID, LeaseToken: run.LeaseToken,
	})
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Len(t, selected, 1)
	require.Equal(t, first.Evidence[0].FragmentID, selected[0].SourceID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_reconciliation_runs
			SET lease_until = clock_timestamp() - interval '1 second'
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Error
	}))
	reclaimed, reclaimedClaimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: runDate, WorkerID: "search-repair-reclaim-cursor-reclaimer", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, reclaimedClaimed)
	require.Nil(t, reclaimed.SelectionCursor)
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
			signalled := false
			defer func() {
				if !signalled {
					close(teamLocked)
				}
			}()
			var lockedTeamID string
			if err := tx.Raw(`
				SELECT id::text
				FROM teams
				WHERE id = ?::uuid
				FOR UPDATE
			`, firstTeamID).Row().Scan(&lockedTeamID); err != nil {
				return err
			}
			signalled = true
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
