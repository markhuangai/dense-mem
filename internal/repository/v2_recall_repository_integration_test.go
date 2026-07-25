package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2RecallEvidenceHydratesSupportDecisionEligibility(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "recall-support-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "recall-support-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	searchRepo := NewV2SearchRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Jamie")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall support decisions", "Jamie works on the Dense Mem platform.")
	upsertV2RecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	decision := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:support-decisions",
			SpanStart:      0,
			SpanEnd:        len("Jamie works on the Dense Mem platform."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)

	assertV2RecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})

	_, err := semanticRepo.ApplyRelationshipSupportDecision(ctx, V2ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "revoke",
		Reason:         "source no longer supports the relationship",
		IdempotencyKey: "recall-revoke-support",
	})
	require.NoError(t, err)
	assertV2RecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID, nil)

	_, err = semanticRepo.ApplyRelationshipSupportDecision(ctx, V2ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "reinstate",
		Reason:         "source restored",
		IdempotencyKey: "recall-reinstate-support",
	})
	require.NoError(t, err)
	assertV2RecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})
}

func TestV2RecallEvidenceExcludesStaleSupportSourceRevision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "recall-stale-support-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "recall-stale-support-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	searchRepo := NewV2SearchRepository(appDB, rls)

	source, err := ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://recall-support-source",
		SourceKind:     "document",
		Authority:      "primary",
		RevisionToken:  "rev-1",
		ContentHash:    sha256Hex("recall support source rev 1"),
	})
	require.NoError(t, err)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Taylor")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall stale support source", "Taylor maintains the Dense Mem search path.")
	upsertV2RecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	decision := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "maintains",
		ObjectEntityID:  object.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:stale-support-source",
			SpanStart:      0,
			SpanEnd:        len("Taylor maintains the Dense Mem search path."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)
	setV2RelationshipSupportSourceForTest(t, ctx, adminDB, rls, teamID, ownerID, decision.SupportID, source.SourceID, source.SourceRevisionID)

	assertV2RecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})

	advanceV2SupportSourceCurrentRevisionForTest(t, ctx, adminDB, rls, teamID, ownerID, source.SourceID, source.SourceRevisionID)
	assertV2RecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID, nil)
}

func upsertV2RecallEvidenceSearchDocumentForTest(
	t *testing.T,
	ctx context.Context,
	repo *V2SearchRepositoryImpl,
	teamID string,
	ownerID string,
	evidence V2EvidenceFragment,
) {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
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

func assertV2RecallEvidenceRelationships(
	t *testing.T,
	ctx context.Context,
	repo *V2SearchRepositoryImpl,
	teamID string,
	evidenceID string,
	wantRelationshipIDs []string,
) {
	t.Helper()
	recall, err := repo.RecallEvidence(ctx, V2RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense Mem",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, evidenceID, recall.Results[0].EvidenceID)
	assert.ElementsMatch(t, wantRelationshipIDs, recall.Results[0].RelationshipIDs)
}

func setV2RelationshipSupportSourceForTest(
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

func advanceV2SupportSourceCurrentRevisionForTest(
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
