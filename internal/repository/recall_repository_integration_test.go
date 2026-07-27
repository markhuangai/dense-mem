package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallEvidenceHydratesSupportDecisionEligibility(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-support-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-support", 3, "exact", "")
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
	insertSearchTestContract(t, adminDB, rls, "recall-provenance", 3, "exact", "")
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

func TestRecallRelationshipsUsesNullGenerationVectorsForPostCutoverTeam(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-null-generation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-null-generation-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-null-generation", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Kai")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall null generation", "Kai works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-null-generation",
			SpanStart:      0,
			SpanEnd:        len("Kai works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	doc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Kai\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	require.Equal(t, 2, doc.ProjectionFormat)
	require.Empty(t, doc.ProjectionGenerationID)

	siblingTeamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-sibling-team")
	siblingOwnerID := createLedgerProfile(t, adminDB, rls, siblingTeamID, "recall-relationships-sibling-owner")
	siblingSubject := createSemanticEntity(t, ctx, semanticRepo, siblingTeamID, siblingOwnerID, "person", "Sibling Kai")
	siblingObject := createSemanticEntity(t, ctx, semanticRepo, siblingTeamID, siblingOwnerID, "project", "Sibling Project")
	siblingIngest := createSemanticIngest(t, ctx, ledgerRepo, siblingTeamID, siblingOwnerID,
		"relationship recall sibling team", "Sibling Kai works on Sibling Project.")
	siblingDecision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          siblingTeamID,
		OwnerProfileID:  siblingOwnerID,
		IngestID:        siblingIngest.IngestID,
		SubjectEntityID: siblingSubject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  siblingObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     siblingIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-sibling-team",
			SpanStart:      0,
			SpanEnd:        len("Sibling Kai works on Sibling Project."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, siblingDecision.Relationship)
	siblingDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         siblingTeamID,
		OwnerProfileID: siblingOwnerID,
		SourceKind:     "relationship",
		SourceID:       siblingDecision.Relationship.RelationshipID,
		SourceVersion:  int64(siblingDecision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Sibling Kai\npredicate: works on\nobject: Sibling Project\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, siblingTeamID, map[string][]float32{
		siblingDoc.SearchDocumentID: {1, 0, 0},
	})

	var generationCount int64
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM search_projection_generations
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
		`, teamID).Scan(&generationCount).Error
	})
	require.NoError(t, err)
	require.Zero(t, generationCount)

	pendingRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.True(t, pendingRecall.VectorOmitted)
	require.Equal(t, string(domain.SearchProjectionPending), pendingRecall.SearchState)
	require.Empty(t, pendingRecall.Results)

	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})

	currentRecall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.False(t, currentRecall.VectorOmitted)
	require.Equal(t, string(domain.SearchProjectionCurrent), currentRecall.SearchState)
	require.Len(t, currentRecall.Results, 1)
	require.Equal(t, decision.Relationship.RelationshipID, currentRecall.Results[0].RelationshipID)
	require.Equal(t, []string{ingest.Evidence[0].FragmentID}, currentRecall.Results[0].EvidenceIDs)
	require.Equal(t, 1, currentRecall.Results[0].Rank)
	for _, result := range currentRecall.Results {
		require.NotEqual(t, siblingDecision.Relationship.RelationshipID, result.RelationshipID)
	}
}

func TestRecallRelationshipsOmitsVectorWhenOnlyStaleContractDocumentsAreCurrent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-stale-contract-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-stale-contract-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-stale-contract-old", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Noa")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall stale contract", "Noa works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-stale-contract",
			SpanStart:      0,
			SpanEnd:        len("Noa works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	doc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Noa\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})
	insertSearchTestContract(t, adminDB, rls, "recall-rel-stale-contract-new", 3, "exact", "")

	recall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.True(t, recall.VectorOmitted)
	require.Equal(t, string(domain.SearchProjectionPending), recall.SearchState)
	require.Empty(t, recall.Results)
}

func TestRecallRelationshipsUsesCurrentGenerationVectors(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-current-generation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-current-generation-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-current-generation", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mika")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall current generation", "Mika works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-current-generation",
			SpanStart:      0,
			SpanEnd:        len("Mika works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	generationID := uuid.NewString()
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, completed_at, activated_at
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1,
			    2, 'current', 1, 1,
			    1, 0, now(), now()
			)
		`, teamID, generationID).Error
	}))
	doc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		SourceKind:             "relationship",
		SourceID:               decision.Relationship.RelationshipID,
		SourceVersion:          int64(decision.Relationship.Version),
		ProjectionGenerationID: generationID,
		DocumentText:           "relationship\nsubject: Mika\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})

	recall, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.False(t, recall.VectorOmitted)
	require.Equal(t, string(domain.SearchProjectionCurrent), recall.SearchState)
	require.Len(t, recall.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, recall.Results[0].RelationshipID)
	assert.Equal(t, []string{ingest.Evidence[0].FragmentID}, recall.Results[0].EvidenceIDs)
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
		DocumentText:   "relationship\nsubject: Jules\npredicate: fresh foreground marker\nobject: Dense Mem",
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

func TestRecallRelationshipsReturnsEquivalentRelationshipIDs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-equivalent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-equivalent-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-equivalent-other-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-equivalent", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Morgan")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	firstIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall equivalent first", "Morgan works on Dense Mem.")
	first := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-equivalent-first",
			SpanStart:      0,
			SpanEnd:        len("Morgan works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)
	secondIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, otherOwnerID,
		"relationship recall equivalent second", "Morgan works on Dense Mem.")
	second := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  otherOwnerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-equivalent-second",
			SpanStart:      0,
			SpanEnd:        len("Morgan works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, second.Relationship)
	require.Equal(t, first.Relationship.SemanticGroupKey, second.Relationship.SemanticGroupKey)
	require.NotEqual(t, first.Relationship.RelationshipID, second.Relationship.RelationshipID)

	firstDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       first.Relationship.RelationshipID,
		SourceVersion:  int64(first.Relationship.Version),
		DocumentText:   "relationship\nsubject: Morgan\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	secondDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: otherOwnerID,
		SourceKind:     "relationship",
		SourceID:       second.Relationship.RelationshipID,
		SourceVersion:  int64(second.Relationship.Version),
		DocumentText:   "relationship\nsubject: Morgan\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		firstDoc.SearchDocumentID:  {1, 0, 0},
		secondDoc.SearchDocumentID: {1, 0, 0},
	})

	recalled, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:         teamID,
		Query:          "unmatchedtoken",
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.Len(t, recalled.Results, 1)
	resultIDs := map[string]struct{}{
		first.Relationship.RelationshipID:  {},
		second.Relationship.RelationshipID: {},
	}
	_, ok := resultIDs[recalled.Results[0].RelationshipID]
	require.True(t, ok)
	require.Len(t, recalled.Results[0].EquivalentRelationshipIDs, 1)
	if recalled.Results[0].RelationshipID == first.Relationship.RelationshipID {
		assert.Equal(t, []string{second.Relationship.RelationshipID}, recalled.Results[0].EquivalentRelationshipIDs)
	} else {
		assert.Equal(t, []string{first.Relationship.RelationshipID}, recalled.Results[0].EquivalentRelationshipIDs)
	}
}

func TestRecallRelationshipsHydratesHistoricallySupersededAtKnownAt(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-history-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-history-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-history", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Riley")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall history", "Riley works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-history",
			SpanStart:      0,
			SpanEnd:        len("Riley works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	doc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Riley\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})
	knownAt := time.Now().UTC().Add(time.Minute)
	recordedTo := knownAt.Add(time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET status = 'superseded',
			    recorded_to = ?,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, recordedTo, teamID, decision.Relationship.RelationshipID).Error
	}))

	recalled, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:  teamID,
		Query:   "Riley Dense Mem",
		KnownAt: &knownAt,
		Limit:   5,
	})
	require.NoError(t, err)
	require.Len(t, recalled.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, recalled.Results[0].RelationshipID)
	assert.Equal(t, []string{ingest.Evidence[0].FragmentID}, recalled.Results[0].EvidenceIDs)
}

func TestRecallRelationshipsHydratesHistoricallySupportedAtKnownAt(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-relationships-support-history-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-relationships-support-history-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-rel-support-history", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Sam")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"relationship recall support history", "Sam works on Dense Mem.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:relationship-support-history",
			SpanStart:      0,
			SpanEnd:        len("Sam works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)
	doc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Sam\npredicate: works on\nobject: Dense Mem\npolarity: positive",
	})
	require.NoError(t, err)
	completeSearchJobsForTest(t, searchRepo, teamID, map[string][]float32{
		doc.SearchDocumentID: {1, 0, 0},
	})
	knownAt := databaseNowForTest(t, adminDB, rls)
	time.Sleep(10 * time.Millisecond)
	revoke, err := semanticRepo.ApplyRelationshipSupportDecision(ctx, ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "revoke",
		Reason:         "test_support_history",
		IdempotencyKey: "recall-relationship-support-history-revoke",
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.RelationshipStatusPendingEvidence), revoke.ToStatus)
	require.Zero(t, revoke.SupportCount)

	current, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID,
		Query:  "Sam Dense Mem",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Empty(t, current.Results)

	historical, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:  teamID,
		Query:   "Sam Dense Mem",
		KnownAt: &knownAt,
		Limit:   5,
	})
	require.NoError(t, err)
	require.Len(t, historical.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, historical.Results[0].RelationshipID)
	assert.Equal(t, []string{ingest.Evidence[0].FragmentID}, historical.Results[0].EvidenceIDs)

	validHistorical, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID:  teamID,
		Query:   "Sam Dense Mem",
		ValidAt: &knownAt,
		Limit:   5,
	})
	require.NoError(t, err)
	require.Len(t, validHistorical.Results, 1)
	assert.Equal(t, decision.Relationship.RelationshipID, validHistorical.Results[0].RelationshipID)
	assert.Equal(t, []string{ingest.Evidence[0].FragmentID}, validHistorical.Results[0].EvidenceIDs)
}

func TestRecallEvidenceExcludesStaleSupportSourceRevision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-stale-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-stale-support-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-stale-support", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Taylor")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	content := "Taylor works on the Dense Mem search path."
	ingest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "recall stale support source",
		RequestHash:    sha256Hex(content),
		Evidence: []EvidenceInput{{
			Content:                   content,
			SourceType:                "document",
			SourceKey:                 "doc://recall-support-source",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: sha256Hex("recall support source rev 1"),
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:       ingest.Evidence[0].FragmentID,
			SourceGroupKey:   "recall:stale-support-source",
			SourceID:         ingest.Evidence[0].SourceID,
			SourceRevisionID: ingest.Evidence[0].SourceRevisionID,
			SpanStart:        0,
			SpanEnd:          len(content),
			Authority:        "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)

	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID})

	_, err = ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:                        teamID,
		OwnerProfileID:                ownerID,
		SourceKey:                     "doc://recall-support-source",
		SourceKind:                    "document",
		Authority:                     "primary",
		RevisionToken:                 "rev-2",
		ExpectedPreviousRevisionToken: "rev-1",
		ContentHash:                   sha256Hex("recall support source rev 2"),
	})
	require.NoError(t, err)
	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "Dense Mem",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Empty(t, recall.Results)
}

func TestRecallEvidenceExcludesRelationshipAliases(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-alias-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-alias-support-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-alias-support", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	aliasObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem Alias")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall alias support", "Alex works on Dense Mem.")
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])
	canonical := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:alias-support-canonical",
			SpanStart:      0,
			SpanEnd:        len("Alex works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, canonical.Relationship)
	alias := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  aliasObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "recall:alias-support-alias",
			SpanStart:      0,
			SpanEnd:        len("Alex works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, alias.Relationship)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET identity_alias_of_relationship_id = ?::uuid,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, canonical.Relationship.RelationshipID, teamID, alias.Relationship.RelationshipID).Error
	}))

	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{canonical.Relationship.RelationshipID})
}

func TestRecallEvidenceHydratesHistoricallySupportedRelationshipAtKnownAt(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-evidence-support-history-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-evidence-support-history-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-evidence-support-history", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Jordan")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"recall evidence support history", "Jordan works on Dense Mem.")
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
			SourceGroupKey: "recall:evidence-support-history",
			SpanStart:      0,
			SpanEnd:        len("Jordan works on Dense Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.SupportID)

	knownAt := databaseNowForTest(t, adminDB, rls)
	time.Sleep(10 * time.Millisecond)
	revoke, err := semanticRepo.ApplyRelationshipSupportDecision(ctx, ApplyRelationshipSupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		SupportID:      decision.SupportID,
		Decision:       "revoke",
		Reason:         "test_evidence_support_history",
		IdempotencyKey: "recall-evidence-support-history-revoke",
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.RelationshipStatusPendingEvidence), revoke.ToStatus)
	require.Zero(t, revoke.SupportCount)

	assertRecallEvidenceRelationships(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID, nil)
	assertRecallEvidenceRelationshipsAt(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID}, &knownAt, nil)
	assertRecallEvidenceRelationshipsAt(t, ctx, searchRepo, teamID, ingest.Evidence[0].FragmentID,
		[]string{decision.Relationship.RelationshipID}, nil, &knownAt)
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
	assertRecallEvidenceRelationshipsAt(t, ctx, repo, teamID, evidenceID, wantRelationshipIDs, nil, nil)
}

func assertRecallEvidenceRelationshipsAt(
	t *testing.T,
	ctx context.Context,
	repo *SearchRepositoryImpl,
	teamID string,
	evidenceID string,
	wantRelationshipIDs []string,
	knownAt *time.Time,
	validAt *time.Time,
) {
	t.Helper()
	recall, err := repo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID:  teamID,
		Query:   "Dense Mem",
		KnownAt: knownAt,
		ValidAt: validAt,
		Limit:   5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, evidenceID, recall.Results[0].EvidenceID)
	assert.ElementsMatch(t, wantRelationshipIDs, recall.Results[0].RelationshipIDs)
}

func databaseNowForTest(
	t *testing.T,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
) time.Time {
	t.Helper()
	var now time.Time
	err := rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT now()`).Row().Scan(&now)
	})
	require.NoError(t, err)
	return now.UTC()
}

func insertRelationshipProjectionGenerationForTest(
	t *testing.T,
	db *gorm.DB,
	rls interface {
		WithTeamTx(context.Context, *gorm.DB, string, func(*gorm.DB) error) error
	},
	teamID string,
	generationID string,
	generation int,
	state string,
) {
	t.Helper()
	require.NoError(t, rls.WithTeamTx(context.Background(), db, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, completed_at, activated_at
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', ?,
			    2, ?, 1, 1,
			    1, 0, now(), now()
			)
		`, teamID, generationID, generation, state).Error
	}))
}
