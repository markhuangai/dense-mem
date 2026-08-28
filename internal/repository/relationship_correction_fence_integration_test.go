package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipCorrectionRejectsStaleVersionAfterPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-stale-version", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-stale-version")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Stale Version Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Stale Version Wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Stale Version Correct")
	content := "Stale Version Owner works on Stale Version Correct."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-stale-version", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "stale-version", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "stale-version-submit",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	sourceDocument, err := NewSearchRepository(appDB, rls).UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: original.RelationshipID,
		SourceVersion: int64(original.Version), ProjectionFormat: 2, DocumentText: plan.Documents[0].DocumentText,
		SpaceID: original.SpaceID, SpaceGeneration: original.SpaceGeneration,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", sourceDocument.SearchState)

	var beforeSearchState, beforeDocumentHash string
	var beforeJobCount, beforeRelationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&beforeSearchState, &beforeDocumentHash); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&beforeJobCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&beforeRelationshipCount).Error
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE relationship_records SET version = version + 1, updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, original.RelationshipID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
	require.NoError(t, err)
	require.Equal(t, "rejected", result.ProcessingState)
	require.Equal(t, "relationship_version_stale", result.ErrorCode)
	require.Nil(t, result.Correction)

	var afterSearchState, afterDocumentHash, originalStatus string
	var afterJobCount, afterRelationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&afterSearchState, &afterDocumentHash); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&afterJobCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&afterRelationshipCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Scan(&originalStatus).Error
	}))
	require.Equal(t, "active", originalStatus)
	require.Equal(t, beforeSearchState, afterSearchState)
	require.Equal(t, beforeDocumentHash, afterDocumentHash)
	require.Equal(t, beforeJobCount, afterJobCount)
	require.Equal(t, beforeRelationshipCount, afterRelationshipCount)
}

func TestRelationshipCorrectionRejectsSupportRevisionChangeAfterPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-support-revision", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-support-revision")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Support Revision Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Support Revision Wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Support Revision Correct")
	content := "Support Revision Owner works on Support Revision Correct."
	const sourceKey = "document://relationship-correction-support-revision"
	ingest, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{{
			Content: content, SourceKey: sourceKey, SourceRevisionToken: "rev-1",
			SourceRevisionContentHash: sha256Hex(content),
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	support := &EvidenceSupportInput{
		FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "support-revision",
		SourceID: ingest.Evidence[0].SourceID, SourceRevisionID: ingest.Evidence[0].SourceRevisionID,
		SpanStart: 0, SpanEnd: len(content), Authority: "primary",
	}
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: support,
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "support-revision-submit",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	sourceDocument, err := NewSearchRepository(appDB, rls).UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: original.RelationshipID,
		SourceVersion: int64(original.Version), ProjectionFormat: 2, DocumentText: plan.Documents[0].DocumentText,
		SpaceID: original.SpaceID, SpaceGeneration: original.SpaceGeneration,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", sourceDocument.SearchState)

	var beforeSearchState, beforeDocumentHash string
	var beforeJobCount, beforeRelationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&beforeSearchState, &beforeDocumentHash); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&beforeJobCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&beforeRelationshipCount).Error
	}))

	_, err = ledger.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: sourceKey,
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: sha256Hex("Support Revision Owner now works elsewhere."),
	})
	require.NoError(t, err)

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
	require.NoError(t, err)
	require.Equal(t, "rejected", result.ProcessingState)
	require.Equal(t, "support_set_mismatch", result.ErrorCode)
	require.Nil(t, result.Correction)

	var afterSearchState, afterDocumentHash, originalStatus string
	var afterJobCount, afterRelationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&afterSearchState, &afterDocumentHash); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&afterJobCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&afterRelationshipCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Scan(&originalStatus).Error
	}))
	require.Equal(t, "active", originalStatus)
	require.Equal(t, beforeSearchState, afterSearchState)
	require.Equal(t, beforeDocumentHash, afterDocumentHash)
	require.Equal(t, beforeJobCount, afterJobCount)
	require.Equal(t, beforeRelationshipCount, afterRelationshipCount)
}
