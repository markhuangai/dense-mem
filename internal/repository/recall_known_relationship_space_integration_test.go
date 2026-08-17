package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallRelationshipsScopesKnownRelationshipGroupsToActiveSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "recall-known-group-space-team"))
	ownerID := createLedgerProfile(t, adminDB, rls, teamID.String(), "recall-known-group-space-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID.String(), "recall-known-group-space-other-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-known-group-space", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID.String(), ownerID, "person", "Morgan")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID.String(), ownerID, "project", "Dense Mem")
	firstIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID.String(), ownerID,
		"known-group-space-shared", "Morgan works on Dense Mem.")
	first := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:known-group-space-shared",
			SpanStart:      0,
			SpanEnd:        len("Morgan works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)

	secondIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID.String(), otherOwnerID,
		"known-group-space-private", "Morgan works on Dense Mem.")
	second := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  otherOwnerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:known-group-space-private",
			SpanStart:      0,
			SpanEnd:        len("Morgan works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, second.Relationship)
	require.Equal(t, first.Relationship.SemanticGroupKey, second.Relationship.SemanticGroupKey)

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, teamID, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, privateSpace)
	firstDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       first.Relationship.RelationshipID,
		SourceVersion:  int64(first.Relationship.Version),
		DocumentText:   "relationship\nsubject: Morgan\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	secondDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID.String(),
		OwnerProfileID: otherOwnerID,
		SourceKind:     "relationship",
		SourceID:       second.Relationship.RelationshipID,
		SourceVersion:  int64(second.Relationship.Version),
		DocumentText:   "relationship\nsubject: Morgan\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID.String(), map[string][]float32{
		firstDoc.SearchDocumentID:  {1, 0, 0},
		secondDoc.SearchDocumentID: {1, 0, 0},
	})
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, privateSpace.ID, teamID, second.Relationship.RelationshipID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, privateSpace.ID, teamID, secondDoc.SearchDocumentID).Error
	}))

	recalled, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:               teamID.String(),
		Query:                "unmatchedtoken",
		QueryEmbedding:       []float32{1, 0, 0},
		KnownRelationshipIDs: []string{first.Relationship.RelationshipID},
		Limit:                5,
		SpaceID:              privateSpace.ID.String(),
		SpaceKind:            string(domain.MemorySpaceCredentialPrivate),
	})
	require.NoError(t, err)
	require.Len(t, recalled.Results, 1)
	assert.Equal(t, second.Relationship.RelationshipID, recalled.Results[0].RelationshipID)
}
