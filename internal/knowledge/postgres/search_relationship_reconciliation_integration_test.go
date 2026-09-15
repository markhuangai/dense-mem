//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchReconciliationSelectionFillsRelationshipCapacity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-reconciliation-relationship-cap")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-reconciliation-relationship-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "reconciliation-relationship-cap", 2, "exact", "")
	ledger := NewStore(appDB, rls, ConflictRuntimeConfig{})
	semantic := NewStore(appDB, rls, ConflictRuntimeConfig{})
	repo := newSearchFixtureStore(appDB, rls)

	staleIngest, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-reconciliation-relationship-cap-stale",
		RequestHash: sha256Hex("stale evidence"), Evidence: []EvidenceInput{{Content: "stale evidence"}},
	})
	require.NoError(t, err)
	staleDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: staleIngest.Evidence[0].FragmentID, SourceVersion: 1,
		DocumentText: "outdated stale evidence", EmbeddingContractID: contractID,
	})
	require.NoError(t, err)

	relationshipIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"search-reconciliation-relationship-cap-source", "Jamie works on Dense-Mem.")
	relationshipDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: relationshipIngest.Evidence[0].FragmentID, SourceVersion: 1,
		DocumentText: "Jamie works on Dense-Mem.", EmbeddingContractID: contractID,
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[0.6,0.8]'::vector,
			    embedding_updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, relationshipDocument.SearchDocumentID).Error
	}))

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: relationshipIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     relationshipIngest.Evidence[0].FragmentID,
			SourceGroupKey: "search-reconciliation-relationship-cap", SpanStart: 0,
			SpanEnd: len("Jamie works on Dense-Mem."), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	selected, err := repo.SelectSearchReconciliationDocuments(ctx, SearchReconciliationSelectionInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, selected, 2)
	var foundStale bool
	var foundRelationship bool
	for _, item := range selected {
		if item.SearchDocumentID == staleDocument.SearchDocumentID && item.SourceID == staleIngest.Evidence[0].FragmentID {
			foundStale = true
		}
		if item.SourceKind == "relationship" && item.SourceID == decision.Relationship.RelationshipID {
			foundRelationship = true
		}
	}
	require.True(t, foundStale)
	require.True(t, foundRelationship)
}

func TestSearchConvergenceHydratesRelationshipDocumentsAfterClosingDocumentRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-convergence-relationship")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-convergence-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "convergence-relationship", 2, "exact", "")
	ledger := NewStore(appDB, rls, ConflictRuntimeConfig{})
	semantic := NewStore(appDB, rls, ConflictRuntimeConfig{})
	repo := newSearchFixtureStore(appDB, rls)

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
