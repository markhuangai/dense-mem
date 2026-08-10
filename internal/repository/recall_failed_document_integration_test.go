package repository

import (
	"context"
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
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Riley")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall failed lexical", "Riley verifies failurelexicalevidence for Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:failed-lexical",
			SpanStart:      0,
			SpanEnd:        len("Riley verifies failurelexicalevidence for Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	evidenceDocument, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       ingest.Evidence[0].FragmentID,
		SourceVersion:  1,
		DocumentText:   ingest.Evidence[0].Content,
	})
	require.NoError(t, err)
	relationshipDocument, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship failurelexicalrelationship Riley works on Dense Mem",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding = '[1,0,0]'::vector,
			    embedding_error = 'embedding provider timed out'
			WHERE team_id = ?::uuid
			  AND search_document_id IN (?::uuid, ?::uuid)
		`, teamID, evidenceDocument.SearchDocumentID, relationshipDocument.SearchDocumentID).Error
	}))

	evidenceRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "failurelexicalevidence", Limit: 5,
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceRecall.SearchState)
	require.Len(t, evidenceRecall.Results, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, evidenceRecall.Results[0].EvidenceID)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceRecall.Results[0].SearchState)

	relationshipRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID, Query: "failurelexicalrelationship", Limit: 5,
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionFailed), relationshipRecall.SearchState)
	require.Len(t, relationshipRecall.Results, 1)
	require.Equal(t, decision.Relationship.RelationshipID, relationshipRecall.Results[0].RelationshipID)
	require.Equal(t, string(domain.SearchProjectionFailed), relationshipRecall.Results[0].SearchState)

	evidenceVectorRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "vectoronlynomatchtoken", QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, evidenceVectorRecall.Results)
	require.Equal(t, string(domain.SearchProjectionFailed), evidenceVectorRecall.SearchState,
		"failed eligible documents must remain visible in recall state even without a matching hit")
	relationshipVectorRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID, Query: "vectoronlynomatchtoken", QueryEmbedding: []float32{1, 0, 0}, Limit: 5,
	})
	require.NoError(t, err)
	require.True(t, relationshipVectorRecall.VectorOmitted)
	require.Empty(t, relationshipVectorRecall.Results)

	otherTeamID := createLedgerTeam(t, adminDB, rls, "recall-failed-other-team")
	createLedgerProfile(t, adminDB, rls, otherTeamID, "recall-failed-other-owner")
	otherEvidenceRecall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: otherTeamID, Query: "failurelexicalevidence", Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, otherEvidenceRecall.Results)
	otherRelationshipRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: otherTeamID, Query: "failurelexicalrelationship", Limit: 5,
	})
	require.NoError(t, err)
	require.Empty(t, otherRelationshipRecall.Results)
}
