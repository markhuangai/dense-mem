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

func TestRecallRelationshipsUsesActivatedGenerationWhenNewestGenerationFailed(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-failed-generation-fallback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-failed-generation-fallback-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-failed-generation-fallback", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Nia")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall failed generation fallback", "Nia works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:failed-generation-fallback",
			SpanStart:      0,
			SpanEnd:        len("Nia works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	activatedGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, activatedGenerationID, 1, "current")
	document, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               decision.Relationship.RelationshipID,
		SourceVersion:          int64(decision.Relationship.Version),
		ProjectionGenerationID: activatedGenerationID,
		DocumentText:           "relationship\nsubject: Nia\npredicate: activated fallback marker\nobject: Dense Mem",
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, searchRepo, teamID, map[string][]float32{
		document.SearchDocumentID: {1, 0, 0},
	})

	failedGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, failedGenerationID, 2, "failed")

	recall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatched fallback query",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.False(t, recall.VectorOmitted)
	require.Equal(t, "current", recall.SearchState)
	require.Len(t, recall.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, recall.Results[0].RelationshipID)
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

	staleFullText, err := searchRepo.SearchFullText(ctx, FullTextSearchInput{
		TeamID:     teamID,
		Query:      "stale generation marker",
		SourceKind: "relationship",
		Limit:      5,
	})
	require.NoError(t, err)
	require.Empty(t, staleFullText)

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

	freshFullText, err := searchRepo.SearchFullText(ctx, FullTextSearchInput{
		TeamID:     teamID,
		Query:      "fresh foreground marker",
		SourceKind: "relationship",
		Limit:      5,
	})
	require.NoError(t, err)
	require.Len(t, freshFullText, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, freshFullText[0].SourceID)
}

func TestSearchReadinessRejectsStaleRelationshipProjectionGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-readiness-generation-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-readiness-generation-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-readiness-generation-fence", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	repo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Noel")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"search readiness generation fence", "Noel works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "search:readiness-generation-fence",
			SpanStart:      0,
			SpanEnd:        len("Noel works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	staleGenerationID := uuid.NewString()
	currentGenerationID := uuid.NewString()
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, staleGenerationID, 1, "failed")
	insertRelationshipProjectionGenerationForTest(t, appDB, rls, teamID, currentGenerationID, 2, "current")
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               decision.Relationship.RelationshipID,
		SourceVersion:          int64(decision.Relationship.Version),
		ProjectionGenerationID: staleGenerationID,
		DocumentText:           "relationship\nsubject: Noel\npredicate: stale projection\nobject: Dense Mem",
	})
	require.NoError(t, err)

	readiness, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	assert.Contains(t, searchReadinessReasonCodes(readiness), "relationship_projection_text_incomplete")

	_, err = repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Noel\npredicate: foreground projection\nobject: Dense Mem",
		Metadata: map[string]any{
			relationshipForegroundRecallGenerationMetadataKey: currentGenerationID,
		},
	})
	require.NoError(t, err)

	readiness, err = repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	assert.NotContains(t, searchReadinessReasonCodes(readiness), "relationship_projection_text_incomplete")
}

func searchReadinessReasonCodes(readiness *SearchReadiness) []string {
	codes := make([]string, 0, len(readiness.Reasons))
	for _, reason := range readiness.Reasons {
		codes = append(codes, reason.Code)
	}
	return codes
}
