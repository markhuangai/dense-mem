//go:build integration

package postgres

import (
	"context"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallFailedDocumentsRemainLexicalAndTeamScopedWhileVectorsAreExcluded(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-failed-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-failed-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-failed", 3, "exact", "")
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls)
	searchRepo := newSearchFixtureStore(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall failed lexical", "Riley verifies failurelexicalevidence for Dense Mem.")
	evidenceDocument, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       ingest.Evidence[0].FragmentID,
		SourceVersion:  1,
		DocumentText:   ingest.Evidence[0].Content,
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding = '[1,0,0]'::vector,
			    embedding_error = 'embedding provider timed out'
			WHERE team_id = ?::uuid
			  AND search_document_id = ?::uuid
		`, teamID, evidenceDocument.SearchDocumentID).Error
	}))

	evidenceRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "failurelexicalevidence", Limit: 5,
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceRecall.SearchState)
	require.Len(t, evidenceRecall.Results, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, evidenceRecall.Results[0].EvidenceID)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceRecall.Results[0].SearchState)

	evidenceVectorRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "vectoronlynomatchtoken", QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, evidenceVectorRecall.Results)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceVectorRecall.SearchState,
		"failed eligible documents must remain visible in recall state even without a matching hit")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "recall-failed-other-team")
	createLedgerProfile(t, adminDB, rls, otherTeamID, "recall-failed-other-owner")
	otherEvidenceRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: otherTeamID, Query: "failurelexicalevidence", Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, otherEvidenceRecall.Results)
}

func TestRecallExcludesConflictResolutionDeletionOnlyEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-deletion-only")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-deletion-only-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "recall-deletion-only", 2, "exact", "")
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls)
	searchRepo := newSearchFixtureStore(appDB, rls)
	marker := map[string]any{"conflict_resolution_deletion_only": true}
	content := "overdue conflict deletion-only derivation recall sentinel"
	ingest, err := createTestIngest(ctx, ledgerRepo, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-deletion-only",
		RequestHash: sha256Hex(content), Status: "completed", Metadata: marker,
		Evidence: []EvidenceInput{{Content: content, Metadata: marker}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	document, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: ingest.Evidence[0].FragmentID, SourceVersion: 1,
		DocumentText: content, EmbeddingContractID: contractID,
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[1,0]'::vector,
			    embedding_updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))

	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "deletion-only derivation recall sentinel",
		QueryEmbedding: []float32{1, 0}, Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, recall.Results)
	require.Equal(t, string(domain.SearchProjectionCurrent), recall.SearchState)
}
