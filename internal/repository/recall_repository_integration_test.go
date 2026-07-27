package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallEvidenceHydratesSupportDecisionEligibility(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-support-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Jamie")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall support decisions", "Jamie works on the Dense Mem platform.")
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:support-decisions",
			SpanStart:      0,
			SpanEnd:        len("Jamie works on the Dense Mem platform."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)

	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})

	_, err := semanticRepo.ApplyRelationshipSupportDecision(ctx, ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "revoke",
		Reason:         "source no longer supports the relationship",
		IdempotencyKey: "recall-revoke-support",
	})
	require.NoError(t, err)
	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID, nil)

	_, err = semanticRepo.ApplyRelationshipSupportDecision(ctx, ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "reinstate",
		Reason:         "source restored",
		IdempotencyKey: "recall-reinstate-support",
	})
	require.NoError(t, err)
	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})
}

func TestRecallEvidenceHydratesEvidenceProvenance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-provenance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-provenance-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)
	content := "Dense Mem records recall provenance for evidence hits."

	ingest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "recall-provenance",
		RequestHash:    sha256Hex(content),
		Evidence: []EvidenceInput{{
			Content:    content,
			SourceType: "document",
			SourceRef:  "wiki:recall-provenance",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])

	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "recall provenance",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	require.Equal(t, "wiki:recall-provenance", recall.Results[0].Source)
	require.Equal(t, "document", recall.Results[0].SourceType)
	require.False(t, recall.Results[0].CreatedAt.IsZero())
}

func TestRecallEvidenceExcludesStaleSupportSourceRevision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-stale-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-stale-support-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	source, err := ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://recall-support-source",
		SourceKind:     "document",
		Authority:      "primary",
		RevisionToken:  "rev-1",
		ContentHash:    sha256Hex("recall support source rev 1"),
	})
	require.NoError(t, err)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Taylor")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall stale support source", "Taylor maintains the Dense Mem search path.")
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "maintains",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:stale-support-source",
			SpanStart:      0,
			SpanEnd:        len("Taylor maintains the Dense Mem search path."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)
	setRelationshipSupportSourceForTest(t, ctx, adminDB, rls, teamID, ownerID, decision.SupportID, source.SourceID, source.SourceRevisionID)

	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})

	advanceSupportSourceCurrentRevisionForTest(t, ctx, adminDB, rls, teamID, ownerID, source.SourceID, source.SourceRevisionID)
	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID, nil)
}

func upsertRecallEvidenceSearchDocumentForTest(
	t *testing.T,
	ctx context.Context,
	repo *SearchRepositoryImpl,
	teamID string,
	ownerID string,
	evidence EvidenceFragment,
) {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       evidence.FragmentID,
		SourceVersion:  1,
		DocumentText:   evidence.Content,
	})
	require.NoError(t, err)
	require.Equal(t, evidence.FragmentID, doc.SourceID)
	require.Equal(t, "pending", doc.SearchState)
}

func assertRecallEvidenceRelationships(
	t *testing.T,
	ctx context.Context,
	repo *SearchRepositoryImpl,
	teamID string,
	evidenceID string,
	wantRelationshipIDs []string,
) {
	t.Helper()
	recall, err := repo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense Mem",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, evidenceID, recall.Results[0].EvidenceID)
	assert.ElementsMatch(t, wantRelationshipIDs, recall.Results[0].RelationshipIDs)
}

func setRelationshipSupportSourceForTest(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	supportID string,
	sourceID string,
	sourceRevisionID string,
) {
	t.Helper()
	err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_evidence_supports
			SET source_id = ?::uuid,
			    source_revision_id = ?::uuid
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND support_id = ?::uuid
		`, sourceID, sourceRevisionID, teamID, ownerID, supportID).Error
	})
	require.NoError(t, err)
}

func advanceSupportSourceCurrentRevisionForTest(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	sourceID string,
	previousRevisionID string,
) {
	t.Helper()
	nextRevisionID := uuid.NewString()
	err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO evidence_source_revisions (
			    team_id, source_revision_id, source_id, owner_profile_id,
			    revision_token, expected_previous_revision_token,
			    supersedes_revision_id, content_hash
			)
			VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'rev-2', 'rev-1',
			    ?::uuid, ?
			)
		`, teamID, nextRevisionID, sourceID, ownerID, previousRevisionID, sha256Hex("recall support source rev 2")).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE evidence_sources
			SET current_revision_id = ?::uuid,
			    current_revision_token = 'rev-2',
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND source_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, nextRevisionID, teamID, sourceID, ownerID).Error
	})
	require.NoError(t, err)
}
