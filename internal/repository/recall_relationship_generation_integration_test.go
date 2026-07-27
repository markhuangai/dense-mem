package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecallRelationshipsKeepsLastActivatedGenerationDuringUnactivatedProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-activated-generation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-activated-generation-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-activated-generation", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Iris")
	activeObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Stable Dense Mem")
	activeIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall activated generation", "Iris works on Stable Dense Mem.")
	activeDecision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        activeIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  activeObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     activeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:activated-generation",
			SpanStart:      0,
			SpanEnd:        len("Iris works on Stable Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, activeDecision.Relationship)

	unactivatedObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Preview Dense Mem")
	unactivatedIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall unactivated generation", "Iris works on Preview Dense Mem.")
	unactivatedDecision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        unactivatedIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  unactivatedObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     unactivatedIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:unactivated-generation",
			SpanStart:      0,
			SpanEnd:        len("Iris works on Preview Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, unactivatedDecision.Relationship)

	activatedGenerationID := uuid.NewString()
	unactivatedGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, activatedGenerationID, 1, "current")
	_, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               activeDecision.Relationship.RelationshipID,
		SourceVersion:          int64(activeDecision.Relationship.Version),
		ProjectionGenerationID: activatedGenerationID,
		DocumentText:           "relationship\nsubject: Iris\npredicate: activated generation marker\nobject: Stable Dense Mem",
	})
	require.NoError(t, err)
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, unactivatedGenerationID, 2, "embedding")
	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               unactivatedDecision.Relationship.RelationshipID,
		SourceVersion:          int64(unactivatedDecision.Relationship.Version),
		ProjectionGenerationID: unactivatedGenerationID,
		DocumentText:           "relationship\nsubject: Iris\npredicate: unactivated generation marker\nobject: Preview Dense Mem",
	})
	require.NoError(t, err)

	activeRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "activated generation marker",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, activeRecall.Results, 1)
	assert.Equal(t, activeDecision.Relationship.RelationshipID, activeRecall.Results[0].RelationshipID)
	assert.Equal(t, "pending", activeRecall.SearchState)

	unactivatedRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "unactivated generation marker",
		Limit:  5,
	})
	require.NoError(t, err)
	assert.Empty(t, unactivatedRecall.Results)
	assert.Equal(t, "pending", unactivatedRecall.SearchState)
}

func TestRecallRelationshipsFullTextFencesGenerationAndKeepsForegroundRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-generation-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-generation-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-generation-fence", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Jules")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall generation fence", "Jules works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-generation-fence",
			SpanStart:      0,
			SpanEnd:        len("Jules works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	oldGenerationID := uuid.NewString()
	newGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, oldGenerationID, 1, "failed")
	_, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               decision.Relationship.RelationshipID,
		SourceVersion:          int64(decision.Relationship.Version),
		ProjectionGenerationID: oldGenerationID,
		DocumentText:           "relationship\nsubject: Jules\npredicate: stale generation marker\nobject: Dense Mem",
	})
	require.NoError(t, err)
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, newGenerationID, 2, "current")

	stale, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "stale generation marker",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Empty(t, stale.Results)

	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Jules\npredicate: stale null marker\nobject: Dense Mem",
	})
	require.NoError(t, err)
	staleNull, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "stale null marker",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Empty(t, staleNull.Results)

	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Jules\npredicate: fresh foreground marker\nobject: Dense Mem",
		Metadata: map[string]any{
			relationshipForegroundRecallGenerationMetadataKey: newGenerationID,
		},
	})
	require.NoError(t, err)
	fresh, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "fresh foreground marker",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, fresh.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, fresh.Results[0].RelationshipID)
}
