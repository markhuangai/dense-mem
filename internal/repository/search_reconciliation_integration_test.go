//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchReconciliationSelectionAndHashFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-reconciliation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-reconciliation-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "reconciliation", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-reconciliation-evidence",
		RequestHash: sha256Hex("canonical rendered document"), Evidence: []EvidenceInput{{Content: "canonical rendered document"}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)

	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: ingest.Evidence[0].FragmentID, SourceVersion: 1,
		DocumentText: "stale rendered document",
	})
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchReconciliationRun(ctx, SearchReconciliationRunInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)

	selected, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 256,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, "canonical rendered document", selected[0].DocumentText)
	require.NotEqual(t, selected[0].StoredDocumentHash, selected[0].DocumentHash)
	oldHash := selected[0].DocumentHash

	embedding := SearchDocumentEmbedding{
		TeamID: selected[0].TeamID, SearchDocumentID: selected[0].SearchDocumentID,
		OwnerProfileID: selected[0].OwnerProfileID, DocumentText: selected[0].DocumentText,
		DocumentHash: selected[0].DocumentHash, StoredDocumentHash: selected[0].StoredDocumentHash,
		SourceVersion: selected[0].SourceVersion, ProjectionFormat: selected[0].ProjectionFormat,
		ProjectionGenerationID: selected[0].ProjectionGenerationID, DocumentVersion: selected[0].DocumentVersion,
		EmbeddingContractID: selected[0].EmbeddingContractID, EmbeddingDimensions: selected[0].EmbeddingDimensions,
		Embedding: []float32{0.2, 0.8}, SpaceID: selected[0].SpaceID, SpaceGeneration: selected[0].SpaceGeneration,
	}
	apply, err := repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []SearchDocumentEmbedding{embedding},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.Zero(t, apply.SkippedCount)
	require.Zero(t, apply.RemainingDriftedCount)

	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: document.SourceID, SourceVersion: 2, DocumentText: "new rendered document",
	})
	require.NoError(t, err)
	selectedAfterUpdate, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 256,
	})
	require.NoError(t, err)
	require.Len(t, selectedAfterUpdate, 1)
	require.Equal(t, oldHash, selectedAfterUpdate[0].DocumentHash)
	require.NotEqual(t, selectedAfterUpdate[0].StoredDocumentHash, selectedAfterUpdate[0].DocumentHash)

	staleApply, err := repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []SearchDocumentEmbedding{embedding},
	})
	require.NoError(t, err)
	require.Zero(t, staleApply.UpdatedCount)
	require.EqualValues(t, 1, staleApply.SkippedCount)
	require.EqualValues(t, 1, staleApply.RemainingDriftedCount)

	freshEmbedding := embedding
	freshEmbedding.DocumentVersion = selectedAfterUpdate[0].DocumentVersion
	freshEmbedding.StoredDocumentHash = selectedAfterUpdate[0].StoredDocumentHash
	apply, err = repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []SearchDocumentEmbedding{freshEmbedding},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.Zero(t, apply.SkippedCount)
	require.Zero(t, apply.RemainingDriftedCount)

	require.NoError(t, repo.FinishSearchReconciliationRun(ctx, FinishSearchReconciliationRunInput{
		RunID: run.RunID, Status: "completed", SelectedCount: 1, EmbeddedCount: 1,
		UpdatedCount: apply.UpdatedCount, DriftedCount: apply.RemainingDriftedCount,
	}))
}
func TestSearchReconciliationCanonicalSourceSetAndSpaceFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-reconciliation-source-set")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-reconciliation-source-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "reconciliation-source-set", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	ingest, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-reconciliation-source-set",
		RequestHash: sha256Hex("source-set canonical evidence"), Evidence: []EvidenceInput{{Content: "source-set canonical evidence"}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)

	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, convergence.ExpectedDocuments)
	require.EqualValues(t, 1, convergence.DriftedDocuments)
	require.Contains(t, convergence.DriftClasses, SearchDocumentDriftCount{Class: "canonical_document_missing", Count: 1})

	selected, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 256,
	})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, selected[0].SourceID)
	require.Empty(t, selected[0].SearchDocumentID)
	apply, err := repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
		Documents: []SearchDocumentEmbedding{{
			TeamID: selected[0].TeamID, SearchDocumentID: selected[0].SearchDocumentID,
			OwnerProfileID: selected[0].OwnerProfileID, DocumentText: selected[0].DocumentText,
			DocumentHash: selected[0].DocumentHash, StoredDocumentHash: selected[0].StoredDocumentHash,
			SourceVersion: selected[0].SourceVersion, ProjectionFormat: selected[0].ProjectionFormat,
			ProjectionGenerationID: selected[0].ProjectionGenerationID, DocumentVersion: selected[0].DocumentVersion,
			EmbeddingContractID: selected[0].EmbeddingContractID, EmbeddingDimensions: selected[0].EmbeddingDimensions,
			Embedding: []float32{0.3, 0.7}, SpaceID: selected[0].SpaceID, SpaceGeneration: selected[0].SpaceGeneration,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.Zero(t, apply.RemainingDriftedCount)
	loaded, err := repo.LoadSearchDocumentsForSources(ctx, LoadSearchDocumentsForSourcesInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceIDs: []string{ingest.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	selected[0] = loaded[0]

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET space_id = ?::uuid, space_generation = ?
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, privateSpace.ID, privateGeneration, teamID, selected[0].SearchDocumentID).Error
	}))

	spaceDrift, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 256,
	})
	require.NoError(t, err)
	require.Len(t, spaceDrift, 1)
	require.Equal(t, selected[0].SpaceID, spaceDrift[0].SpaceID)
	spaceApply := SearchDocumentEmbedding{
		TeamID: spaceDrift[0].TeamID, SearchDocumentID: spaceDrift[0].SearchDocumentID,
		OwnerProfileID: spaceDrift[0].OwnerProfileID, DocumentText: spaceDrift[0].DocumentText,
		DocumentHash: spaceDrift[0].DocumentHash, StoredDocumentHash: spaceDrift[0].StoredDocumentHash,
		SourceVersion: spaceDrift[0].SourceVersion, ProjectionFormat: spaceDrift[0].ProjectionFormat,
		ProjectionGenerationID: spaceDrift[0].ProjectionGenerationID, DocumentVersion: spaceDrift[0].DocumentVersion,
		EmbeddingContractID: spaceDrift[0].EmbeddingContractID, EmbeddingDimensions: spaceDrift[0].EmbeddingDimensions,
		Embedding: []float32{0.3, 0.7}, SpaceID: spaceDrift[0].SpaceID, SpaceGeneration: spaceDrift[0].SpaceGeneration,
	}
	spaceResult, err := repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []SearchDocumentEmbedding{spaceApply},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, spaceResult.UpdatedCount)
	require.Zero(t, spaceResult.RemainingDriftedCount)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_quarantines (team_id, fragment_id, ingest_id, owner_profile_id, status, reason)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'active', 'source retired for reconciliation')
		`, teamID, ingest.Evidence[0].FragmentID, ingest.IngestID, ownerID).Error
	}))
	retired, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 256,
	})
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.True(t, retired[0].Retired)
	var state string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT search_state FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, selected[0].SearchDocumentID).Row().Scan(&state)
	}))
	require.Equal(t, "current", state)
	retiredResult := SearchDocumentEmbedding{
		TeamID: retired[0].TeamID, SearchDocumentID: retired[0].SearchDocumentID,
		OwnerProfileID: retired[0].OwnerProfileID, SourceKind: retired[0].SourceKind,
		SourceID: retired[0].SourceID, DocumentText: retired[0].DocumentText,
		DocumentHash: retired[0].DocumentHash, SourceVersion: retired[0].SourceVersion,
		ProjectionFormat: retired[0].ProjectionFormat, ProjectionGenerationID: retired[0].ProjectionGenerationID,
		DocumentVersion: retired[0].DocumentVersion, EmbeddingContractID: retired[0].EmbeddingContractID,
		EmbeddingDimensions: retired[0].EmbeddingDimensions, SpaceID: retired[0].SpaceID,
		SpaceGeneration: retired[0].SpaceGeneration, Retired: true,
	}
	retiredApply, err := repo.CompleteSearchReconciliationDocuments(ctx, ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
		Documents: []SearchDocumentEmbedding{retiredResult},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, retiredApply.UpdatedCount)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT search_state FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, selected[0].SearchDocumentID).Row().Scan(&state)
	}))
	require.Equal(t, "not_required", state)
}

func TestSearchConvergenceHydratesRelationshipDocumentsAfterClosingDocumentRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-convergence-relationship")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-convergence-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "convergence-relationship", 2, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	repo := NewSearchRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-convergence-relationship", "Jamie works on Dense-Mem.")
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "search-convergence",
			SpanStart:      0,
			SpanEnd:        len("Jamie works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	var documentText string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		var err error
		documentText, err = semanticRelationshipSearchText(ctx, tx, decision.Relationship)
		return err
	}))
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: decision.Relationship.RelationshipID, SourceVersion: int64(decision.Relationship.Version),
		ProjectionFormat: 2, DocumentText: documentText, EmbeddingContractID: contractID,
		SpaceID: decision.Relationship.SpaceID, SpaceGeneration: decision.Relationship.SpaceGeneration,
	})
	require.NoError(t, err)

	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, convergence)
	require.GreaterOrEqual(t, convergence.ExpectedDocuments, int64(1))
	require.GreaterOrEqual(t, convergence.DriftedDocuments, int64(1))
}
