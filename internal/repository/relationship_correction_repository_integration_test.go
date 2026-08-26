package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func loadCorrectionResultForTest(
	t *testing.T,
	ctx context.Context,
	rls *storagepostgres.RLS,
	db *gorm.DB,
	teamID, ownerProfileID, submissionID string,
) (*CorrectRelationshipResult, error) {
	t.Helper()
	var result *CorrectRelationshipResult
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerProfileID, func(tx *gorm.DB) error {
		row, err := loadRelationshipCorrectionSubmission(ctx, tx, teamID, ownerProfileID, submissionID, false)
		if err != nil {
			return err
		}
		result = relationshipCorrectionResultFromRow(row)
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRelationshipCorrectionNotFound
	}
	return result, err
}

func TestRelationshipCorrectionReplacesOwnedRelationshipAndPreservesSupport(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-replace", 3, "exact", "")
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "relationship-correction-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "relationship-correction-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "owner-c")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Wrong Project")
	correctObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Dense-Mem")
	retiredObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Retired Project")
	crossTeamObject := createSemanticEntity(t, ctx, semantic, teamC, ownerC, "project", "Cross-Team Project")
	ingest := createSemanticIngest(t, ctx, ledger, teamA, ownerA, "relationship-correction-source", "Mark Huang works on Dense-Mem.")
	fragmentID := ingest.Evidence[0].FragmentID
	spanEnd := len("Mark Huang works on Dense-Mem.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerA, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: "conversation:correction", SpanStart: 0, SpanEnd: spanEnd, Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)

	request := CorrectRelationshipInput{
		TeamID: teamA, OwnerProfileID: ownerA, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: fragmentID, Start: 0, End: spanEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "replace-owned-relationship",
	}
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamA, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE entity_records SET status = 'retired', updated_at = now()
			WHERE team_id = ?::uuid AND entity_id = ?::uuid
		`, teamA, retiredObject.EntityID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))
	for index, targetID := range []string{uuid.NewString(), retiredObject.EntityID, crossTeamObject.EntityID} {
		unavailable := request
		unavailable.Patch.ObjectEntity = &RelationshipCorrectionEntityPatch{EntityID: targetID}
		unavailable.IdempotencyKey = "reject-unavailable-entity-" + string(rune('a'+index))
		rejected, err := semantic.CorrectRelationship(ctx, unavailable)
		require.NoError(t, err)
		require.Equal(t, "rejected", rejected.ProcessingState, "target index %d id %s", index, targetID)
		require.Equal(t, "entity_not_found", rejected.ErrorCode, "target index %d id %s", index, targetID)
	}

	nonOwner := request
	nonOwner.OwnerProfileID = ownerB
	nonOwner.IdempotencyKey = "replace-other-owner-relationship"
	_, err := semantic.CorrectRelationship(ctx, nonOwner)
	require.ErrorIs(t, err, ErrSemanticOwnerMismatch)

	crossTeam := request
	crossTeam.TeamID = teamC
	crossTeam.OwnerProfileID = ownerC
	crossTeam.IdempotencyKey = "replace-cross-team-relationship"
	_, err = semantic.CorrectRelationship(ctx, crossTeam)
	require.ErrorIs(t, err, ErrSemanticOwnerMismatch)

	result, err := semantic.CorrectRelationship(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, "pending", result.SearchState)
	require.NotNil(t, result.Correction)
	require.False(t, result.Correction.ReusedSuccessor)
	require.NotEqual(t, original.RelationshipID, result.Correction.SuccessorRelationshipID)

	replayed, err := semantic.CorrectRelationship(ctx, request)
	require.NoError(t, err)
	require.Equal(t, result.SubmissionID, replayed.SubmissionID)
	require.Equal(t, result.Correction.SuccessorRelationshipID, replayed.Correction.SuccessorRelationshipID)

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		var originalStatus, successorStatus string
		var originalSupportCount, successorSupportCount int
		if err := tx.Raw(`
			SELECT status, support_count FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamA, original.RelationshipID).Row().Scan(&originalStatus, &originalSupportCount); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status, support_count FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamA, result.Correction.SuccessorRelationshipID).Row().Scan(&successorStatus, &successorSupportCount); err != nil {
			return err
		}
		assert.Equal(t, "superseded", originalStatus)
		assert.Equal(t, 1, originalSupportCount)
		assert.Equal(t, "active", successorStatus)
		assert.Equal(t, 1, successorSupportCount)
		var searchState string
		if err := tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamA, result.Correction.SuccessorRelationshipID).Row().Scan(&searchState); err != nil {
			return err
		}
		assert.Equal(t, searchState, result.SearchState)

		var crossReferences, correctionEvents int64
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_cross_references
			WHERE team_id = ?::uuid AND source_relationship_id = ?::uuid
			  AND target_relationship_id = ?::uuid AND kind = 'corrects'
		`, teamA, result.Correction.SuccessorRelationshipID, original.RelationshipID).Scan(&crossReferences).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_correction_events
			WHERE team_id = ?::uuid AND submission_id = ?::uuid
		`, teamA, result.SubmissionID).Scan(&correctionEvents).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), crossReferences)
		assert.Equal(t, int64(1), correctionEvents)
		return nil
	})
	require.NoError(t, err)

	freshGraph, err := semantic.SemanticGraph(ctx, SemanticGraphQuery{TeamID: teamA, Limit: 181})
	require.NoError(t, err)
	assert.Contains(t, semanticGraphEdgeIDs(freshGraph.Edges), result.Correction.SuccessorRelationshipID)
	assert.NotContains(t, semanticGraphEdgeIDs(freshGraph.Edges), original.RelationshipID)
	assertGraphHasNode(t, freshGraph.Nodes, "entity", correctObject.EntityID, "Dense-Mem")
	assert.NotContains(t, semanticGraphNodeIDs(freshGraph.Nodes), wrongObject.EntityID)

	otherSubject := createSemanticEntity(t, ctx, semantic, teamA, ownerB, "person", "Other Owner")
	otherContent := "Other Owner works on Wrong Project."
	otherIngest := createSemanticIngest(t, ctx, ledger, teamA, ownerB, "relationship-correction-shared-endpoint", otherContent)
	shared := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerB, IngestID: otherIngest.IngestID,
		SubjectEntityID: otherSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: otherIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:correction:shared",
			SpanStart: 0, SpanEnd: len(otherContent), Authority: "primary",
		},
	}).Relationship
	require.NotNil(t, shared)

	sharedGraph, err := semantic.SemanticGraph(ctx, SemanticGraphQuery{TeamID: teamA, Limit: 181})
	require.NoError(t, err)
	assert.Contains(t, semanticGraphEdgeIDs(sharedGraph.Edges), shared.RelationshipID)
	assert.NotContains(t, semanticGraphEdgeIDs(sharedGraph.Edges), original.RelationshipID)
	assertGraphHasNode(t, sharedGraph.Nodes, "entity", wrongObject.EntityID, "Wrong Project")

	_, err = loadCorrectionResultForTest(t, ctx, rls, appDB, teamA, ownerB, result.SubmissionID)
	require.ErrorIs(t, err, ErrRelationshipCorrectionNotFound)
}

func TestRelationshipCorrectionEmbedsOutsideTransactionAndFencesSuccessor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-inline", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-inline-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-inline-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-inline-source", "Mark Huang works on Dense-Mem.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction-inline", SpanStart: 0, SpanEnd: len(ingest.Evidence[0].Content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit", RelationshipID: original.RelationshipID,
		ExpectedVersion: original.Version, Patch: RelationshipCorrectionPatch{
			ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID},
		}, Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(ingest.Evidence[0].Content)}},
		Reason: "the object Entity was resolved incorrectly", IdempotencyKey: "relationship-correction-inline-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 2)
	require.NotEmpty(t, plan.EmbeddingContractID)
	require.NotEmpty(t, plan.SearchIndexGenerationID)
	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, []InlineEmbeddingResult{{
		DocumentHash: plan.Documents[0].DocumentHash, Embedding: []float32{1, 0, 0},
		EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
		EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
	}, {
		DocumentHash: plan.Documents[1].DocumentHash, Embedding: []float32{0, 1, 0},
		EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
		EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
	}})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, string(domain.SearchProjectionCurrent), result.SearchState)

	var searchState string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, result.Correction.SuccessorRelationshipID).Row().Scan(&searchState)
	}))
	assert.Equal(t, string(domain.SearchProjectionCurrent), searchState)
}

func TestRelationshipCorrectionDeduplicatesSameHashEmbeddings(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-same-hash", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-same-hash-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-same-hash-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Same Hash Owner")
	originalObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Same Name")
	successorObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Same Name")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-same-hash-source", "Same Hash Owner works on Same Name.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: originalObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction:same-hash", SpanStart: 0, SpanEnd: len(ingest.Evidence[0].Content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit", RelationshipID: original.RelationshipID,
		ExpectedVersion: original.Version,
		Patch:           RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: successorObject.EntityID}},
		Supports:        []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(ingest.Evidence[0].Content)}},
		Reason:          "replace with the distinct same-named Entity", IdempotencyKey: "relationship-correction-same-hash-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1)

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, []InlineEmbeddingResult{{
		DocumentHash: plan.Documents[0].DocumentHash, Embedding: []float32{1, 0, 0},
		EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
		EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
	}})
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, string(domain.SearchProjectionCurrent), result.SearchState)

	var currentDocuments int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship'
			  AND source_id = ?::uuid AND search_state = 'current'
		`, teamID, result.Correction.SuccessorRelationshipID).Scan(&currentDocuments).Error
	}))
	assert.Equal(t, int64(1), currentDocuments)
}

func TestRelationshipCorrectionPlannerIncludesPredicateVersionChanges(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-predicate-version", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-predicate-version-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-predicate-version-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Predicate Version Owner")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Predicate Version Project")
	content := "Predicate Version Owner works on Predicate Version Project."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-predicate-version-source", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction:predicate-version", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)
	require.Equal(t, 1, original.PredicateVersion)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			)
			SELECT team_id, predicate_key, version + 1, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       'active', 'operator', '{"test":"predicate version rotation"}'::jsonb
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
			  AND predicate_key = 'works_on'
			  AND version = 1
		`, teamID).Error
	}))

	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{Predicate: &RelationshipCorrectionPredicatePatch{Key: "works_on"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "refresh the relationship to the active predicate version", IdempotencyKey: "relationship-correction-predicate-version-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1, "a predicate version change must not be treated as a no-op")
}

func TestRelationshipCorrectionPlannerRejectsSupportMismatchBeforeEmbedding(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-support-mismatch", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-support-mismatch-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-support-mismatch-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Support Mismatch Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Support Mismatch Wrong Project")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Support Mismatch Correct Project")
	content := "Support Mismatch Owner works on Support Mismatch Correct Project."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-support-mismatch-source", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction:support-mismatch", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)

	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content) - 1}},
		Reason:   "the submitted support span is stale", IdempotencyKey: "relationship-correction-support-mismatch-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Empty(t, plan.Documents, "support mismatches must not invoke inline embedding")

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, nil)
	require.NoError(t, err)
	require.Equal(t, "rejected", result.ProcessingState)
	require.Equal(t, "support_set_mismatch", result.ErrorCode)
}

func TestRelationshipCorrectionPlannerRejectsStaleVersionBeforeEmbedding(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-stale-plan", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-stale-plan-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-stale-plan-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Stale Plan Owner")
	oldObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Old Project")
	newObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "New Project")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"relationship-correction-stale-plan-source", "Stale Plan Owner works on Old Project.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: oldObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction:stale-plan",
			SpanStart: 0, SpanEnd: len(ingest.Evidence[0].Content), Authority: "primary",
		},
	}).Relationship
	require.NotNil(t, original)
	var rowsUpdated int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1, updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, original.RelationshipID)
		rowsUpdated = result.RowsAffected
		return result.Error
	}))
	require.Equal(t, int64(1), rowsUpdated)

	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: newObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(ingest.Evidence[0].Content)}},
		Reason:   "the relationship version is stale", IdempotencyKey: "relationship-correction-stale-plan-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Empty(t, plan.Documents)

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, nil)
	require.NoError(t, err)
	require.Equal(t, "rejected", result.ProcessingState)
	require.Equal(t, "relationship_version_stale", result.ErrorCode)
}

func TestRelationshipCorrectionRejectsSearchContractRotationAfterPlanning(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	oldContractID := insertSearchTestContract(t, adminDB, rls, "relationship-correction-rotation-old", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-rotation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-rotation-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	oldObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Old Project")
	newObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "New Project")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-rotation-source", "Mark Huang works on New Project.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: oldObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "correction-rotation", SpanStart: 0, SpanEnd: len(ingest.Evidence[0].Content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit", RelationshipID: original.RelationshipID,
		ExpectedVersion: original.Version, Patch: RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: newObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(ingest.Evidence[0].Content)}},
		Reason:   "rotate the search contract after planning", IdempotencyKey: "relationship-correction-rotation-key",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 2)

	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE search_index_generations SET activation_state = 'retired' WHERE embedding_contract_id = ?::uuid AND activation_state = 'active'`, oldContractID).Error; err != nil {
			return err
		}
		return nil
	}))
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-rotation-new", 3, "exact", "")
	embeddings := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for index, document := range plan.Documents {
		vector := []float32{1, 0, 0}
		if index == 1 {
			vector = []float32{0, 1, 0}
		}
		embeddings = append(embeddings, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
		})
	}
	_, err = semantic.CorrectRelationshipWithEmbeddings(ctx, input, embeddings)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	var status string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Row().Scan(&status)
	}))
	require.Equal(t, string(domain.RelationshipStatusActive), status)
}

func TestRelationshipCorrectionAmbiguityRequiresOneOwnerConfirmation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-ambiguity", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-ambiguity")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	firstAtlas := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	secondAtlas := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-ambiguous-source", "Mark Huang works on Atlas.")
	fragmentID := ingest.Evidence[0].FragmentID
	spanEnd := len("Mark Huang works on Atlas.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: "conversation:ambiguous", SpanStart: 0, SpanEnd: spanEnd, Authority: "primary"},
	}).Relationship
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: fragmentID, Start: 0, End: spanEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "ambiguous-plan",
	})
	require.NoError(t, err)
	require.Empty(t, plan.Documents, "ambiguous submit must not invoke inline embedding")

	submitted, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: fragmentID, Start: 0, End: spanEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "ambiguous-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", submitted.ProcessingState)
	require.NotNil(t, submitted.Confirmation)
	require.Len(t, submitted.Confirmation.Candidates, 2)
	invalidPlan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm", SubmissionID: submitted.SubmissionID,
		ConfirmationToken: uuid.NewString(), Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID},
		IdempotencyKey: "invalid-confirmation-plan",
	})
	require.NoError(t, err)
	require.Empty(t, invalidPlan.Documents, "invalid confirmation credentials must not invoke inline embedding")
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		validationErr := validateSelectedCorrectionEntities(canceledCtx, tx, teamID, submitted.Confirmation.Candidates, RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID})
		require.Error(t, validationErr)
		require.NotErrorIs(t, validationErr, errRelationshipCorrectionSelectionUnavailable)
		return nil
	}))

	var originalStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Scan(&originalStatus).Error
	}))
	require.Equal(t, "active", originalStatus)

	confirmed, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection:      RelationshipCorrectionSelection{ObjectEntityID: strings.ToUpper(firstAtlas.EntityID)},
		IdempotencyKey: "ambiguous-confirm",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", confirmed.ProcessingState)
	require.Equal(t, firstAtlas.EntityID, loadRelationshipObjectEntity(t, ctx, appDB, rls, teamID, ownerID, confirmed.Correction.SuccessorRelationshipID))

	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection:      RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID},
		IdempotencyKey: "second-confirmation-round",
	})
	require.True(t, errors.Is(err, ErrRelationshipCorrectionConfirmation), "err=%v", err)

	expiredSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Expired Confirmation Owner")
	expiredWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Expired Wrong Project")
	expiredIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-expired-source", "Expired Confirmation Owner works on Atlas.")
	expiredSpan := len("Expired Confirmation Owner works on Atlas.")
	expiredOriginal := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: expiredIngest.IngestID,
		SubjectEntityID: expiredSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: expiredWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: expiredIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:expired", SpanStart: 0, SpanEnd: expiredSpan, Authority: "primary"},
	}).Relationship
	expired, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: expiredOriginal.RelationshipID, ExpectedVersion: expiredOriginal.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: expiredIngest.Evidence[0].FragmentID, Start: 0, End: expiredSpan}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "expired-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", expired.ProcessingState)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_correction_submissions
			SET confirmation_expires_at = ?
			WHERE team_id = ?::uuid AND submission_id = ?::uuid
		`, time.Now().UTC().Add(-time.Minute), teamID, expired.SubmissionID).Error
	}))
	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: expired.SubmissionID, ConfirmationToken: expired.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "expired-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmationExpired)
	expiredStatus, err := loadCorrectionResultForTest(t, ctx, rls, appDB, teamID, ownerID, expired.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, "rejected", expiredStatus.ProcessingState)
	require.Equal(t, "confirmation_expired", expiredStatus.ErrorCode)
	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: expired.SubmissionID, ConfirmationToken: expired.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "expired-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmationExpired)

	unavailableSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Unavailable Selection Owner")
	unavailableWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Unavailable Wrong Project")
	unavailableIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-unavailable-source", "Unavailable Selection Owner works on Atlas.")
	unavailableSpan := len("Unavailable Selection Owner works on Atlas.")
	unavailableOriginal := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: unavailableIngest.IngestID,
		SubjectEntityID: unavailableSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: unavailableWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: unavailableIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:unavailable", SpanStart: 0, SpanEnd: unavailableSpan, Authority: "primary"},
	}).Relationship
	unavailable, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: unavailableOriginal.RelationshipID, ExpectedVersion: unavailableOriginal.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: unavailableIngest.Evidence[0].FragmentID, Start: 0, End: unavailableSpan}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "unavailable-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", unavailable.ProcessingState)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE entity_records SET status = 'retired', updated_at = now()
			WHERE team_id = ?::uuid AND entity_id = ?::uuid
		`, teamID, secondAtlas.EntityID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))
	unavailableResult, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: unavailable.SubmissionID, ConfirmationToken: unavailable.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: secondAtlas.EntityID}, IdempotencyKey: "unavailable-confirm",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", unavailableResult.ProcessingState)
	require.Equal(t, "persistent_ambiguity", unavailableResult.ErrorCode)
}

func TestPrivateMemoryCorrectionStatusHidesSealedGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-correction-status-generation"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, identityID, "private-correction-status", domain.CredentialBindingCredentialPrivate)
	semantic := NewSemanticRepository(appDB, rls)
	semanticCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID:  teamID,
		OwnerID: target.ID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: target.TeamSharedSpaceID, Kind: domain.MemorySpaceTeamShared},
			{ID: target.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	subjectID, wrongObjectID := uuid.New(), uuid.New()
	firstCandidateID, secondCandidateID := uuid.New(), uuid.New()
	ingestID, fragmentID := uuid.New(), uuid.New()
	content := "Private correction status evidence."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
			VALUES
			  (?, ?, 'person', ?),
			  (?, ?, 'project', ?),
			  (?, ?, 'project', ?),
			  (?, ?, 'project', ?)
		`, teamID, subjectID, target.MemorySpaceID,
			teamID, wrongObjectID, target.MemorySpaceID,
			teamID, firstCandidateID, target.MemorySpaceID,
			teamID, secondCandidateID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_names (
			    team_id, entity_id, owner_profile_id, display_name,
			    normalized_name, name_kind, space_id
			) VALUES
			  (?, ?, ?, 'Atlas', 'atlas', 'canonical', ?),
			  (?, ?, ?, 'Atlas', 'atlas', 'canonical', ?)
		`, teamID, firstCandidateID, target.ID, target.MemorySpaceID,
			teamID, secondCandidateID, target.ID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
			    team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
			    source_summary, status, proposal, metadata, space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?)
		`, teamID, ingestID, target.ID, "private-correction-status-"+ingestID.String(),
			"hash-"+ingestID.String(), content, target.MemorySpaceID, target.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_fragments (
			    team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
			    content, content_hash, source_type, authority, labels, metadata,
			    space_id, space_generation
			) VALUES (?, ?, ?, ?, 0, ?, ?, 'conversation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)
		`, teamID, fragmentID, ingestID, target.ID, content, "hash-"+fragmentID.String(),
			target.MemorySpaceID, target.MemorySpaceGeneration).Error
	}))

	decision, err := semantic.ApplyRelationshipDecision(semanticCtx, ApplyRelationshipDecisionInput{
		TeamID:            teamID.String(),
		OwnerProfileID:    target.ID.String(),
		IngestID:          ingestID.String(),
		SubjectEntityID:   subjectID.String(),
		PredicateKey:      "works_on",
		PredicateVersion:  1,
		OriginalPredicate: "works_on",
		SubjectRef:        "Private subject",
		ObjectRef:         "Wrong project",
		ObjectEntityID:    wrongObjectID.String(),
		Polarity:          "+",
		EvidenceVerdict:   string(domain.VerificationEntailed),
		Rationale:         "private correction status generation regression",
		Model:             "test",
		ResponseHash:      "hash-private-correction-status",
		Support: &EvidenceSupportInput{
			FragmentID:     fragmentID.String(),
			SourceGroupKey: "private-correction-status-generation",
			SpanStart:      0,
			SpanEnd:        len(content),
			Quote:          content,
			Authority:      "primary",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision.Relationship)

	submitted, err := semantic.CorrectRelationship(semanticCtx, CorrectRelationshipInput{
		TeamID: teamID.String(), OwnerProfileID: target.ID.String(), Action: "submit",
		RelationshipID: decision.Relationship.RelationshipID, ExpectedVersion: decision.Relationship.Version,
		Patch: RelationshipCorrectionPatch{
			ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"},
		},
		Supports:       []RelationshipCorrectionSupport{{EvidenceID: fragmentID.String(), Start: 0, End: len(content)}},
		Reason:         "the object Entity was resolved incorrectly",
		IdempotencyKey: "private-correction-status-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", submitted.ProcessingState)
	require.NotNil(t, submitted.Confirmation)
	require.Len(t, submitted.Confirmation.Candidates, 2)

	status, err := loadCorrectionResultForTest(t, semanticCtx, rls, appDB, teamID.String(), target.ID.String(), submitted.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, submitted.Confirmation.Token, status.Confirmation.Token)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE memory_spaces
			SET generation = generation + 1, lifecycle_state = 'sealed', sealed_at = now()
			WHERE id = ? AND team_id = ? AND lifecycle_state = 'active'
		`, target.MemorySpaceID, teamID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("seal private correction status space: updated %d rows", result.RowsAffected)
		}
		var retained int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_correction_submissions
			WHERE team_id = ? AND submission_id = ? AND space_id = ?
		`, teamID, submitted.SubmissionID, target.MemorySpaceID).Row().Scan(&retained); err != nil {
			return err
		}
		if retained != 1 {
			return fmt.Errorf("expected retained private correction submission, got %d", retained)
		}
		return nil
	}))

	_, err = loadCorrectionResultForTest(t, semanticCtx, rls, appDB, teamID.String(), target.ID.String(), submitted.SubmissionID)
	require.ErrorIs(t, err, ErrRelationshipCorrectionNotFound)
}

func TestRelationshipCorrectionReusesActiveCollisionAndRejectsNoOp(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-collision", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-collision")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	targetObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	sourceIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "collision-source", "Mark works on the wrong project.")
	targetIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "collision-target", "Mark works on Dense-Mem.")
	sourceSpan := len("Mark works on the wrong project.")
	targetSpan := len("Mark works on Dense-Mem.")
	source := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: sourceIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: sourceIngest.Evidence[0].FragmentID, SourceGroupKey: "collision:source", SpanStart: 0, SpanEnd: sourceSpan, Authority: "primary"},
	}).Relationship
	target := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: targetIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: targetObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: targetIngest.Evidence[0].FragmentID, SourceGroupKey: "collision:target", SpanStart: 0, SpanEnd: targetSpan, Authority: "primary"},
	}).Relationship

	reused, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: source.RelationshipID, ExpectedVersion: source.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: targetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: sourceIngest.Evidence[0].FragmentID, Start: 0, End: sourceSpan}},
		Reason:   "replace with the already active Relationship", IdempotencyKey: "reuse-active-collision",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", reused.ProcessingState)
	require.True(t, reused.Correction.ReusedSuccessor)
	require.Equal(t, target.RelationshipID, reused.Correction.SuccessorRelationshipID)

	var supportCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT support_count FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, target.RelationshipID).Scan(&supportCount).Error
	}))
	require.Equal(t, 2, supportCount)

	noOp, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: target.RelationshipID, ExpectedVersion: reused.Correction.SuccessorVersion,
		Patch: RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: targetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{
			{EvidenceID: sourceIngest.Evidence[0].FragmentID, Start: 0, End: sourceSpan},
			{EvidenceID: targetIngest.Evidence[0].FragmentID, Start: 0, End: targetSpan},
		},
		Reason: "this should be rejected as a no-op", IdempotencyKey: "reject-no-op",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", noOp.ProcessingState)
	require.Equal(t, "no_change", noOp.ErrorCode)

	inactiveSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Inactive Collision Owner")
	inactiveWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Inactive Wrong Project")
	inactiveTargetObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Inactive Correct Project")
	inactiveTargetIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "inactive-collision-target", "Inactive Collision Owner works on Inactive Correct Project.")
	inactiveSourceIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "inactive-collision-source", "Inactive Collision Owner works on Inactive Wrong Project.")
	inactiveTargetSpan := len("Inactive Collision Owner works on Inactive Correct Project.")
	inactiveSourceSpan := len("Inactive Collision Owner works on Inactive Wrong Project.")
	inactiveTarget := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: inactiveTargetIngest.IngestID,
		SubjectEntityID: inactiveSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: inactiveTargetObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: inactiveTargetIngest.Evidence[0].FragmentID, SourceGroupKey: "inactive-collision:target", SpanStart: 0, SpanEnd: inactiveTargetSpan, Authority: "primary"},
	}).Relationship
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records SET status = 'superseded', updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, inactiveTarget.RelationshipID).Error
	}))
	inactiveSource := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: inactiveSourceIngest.IngestID,
		SubjectEntityID: inactiveSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: inactiveWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: inactiveSourceIngest.Evidence[0].FragmentID, SourceGroupKey: "inactive-collision:source", SpanStart: 0, SpanEnd: inactiveSourceSpan, Authority: "primary"},
	}).Relationship

	inactiveCollision, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: inactiveSource.RelationshipID, ExpectedVersion: inactiveSource.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: inactiveTargetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: inactiveSourceIngest.Evidence[0].FragmentID, Start: 0, End: inactiveSourceSpan}},
		Reason:   "inactive history must not be revived", IdempotencyKey: "reject-inactive-collision",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", inactiveCollision.ProcessingState)
	require.Equal(t, "inactive_relationship_collision", inactiveCollision.ErrorCode)
}

func loadRelationshipObjectEntity(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID, ownerID, relationshipID string,
) string {
	t.Helper()
	var entityID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT object_entity_id::text FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, relationshipID).Scan(&entityID).Error
	}))
	return entityID
}

func TestCorrectRelationshipValidationRejectsInvalidConfirmation(t *testing.T) {
	input := normalizeCorrectRelationshipInput(CorrectRelationshipInput{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), Action: "confirm",
		SubmissionID: uuid.NewString(), ConfirmationToken: uuid.NewString(), IdempotencyKey: "confirm",
	})
	err := validateCorrectRelationshipInput(input)
	require.ErrorContains(t, err, "selection")
}

func TestNormalizeCorrectRelationshipInputCanonicalizesWithoutMutatingCaller(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	firstEvidenceID := uuid.NewString()
	secondEvidenceID := uuid.NewString()
	callerSupports := []RelationshipCorrectionSupport{
		{EvidenceID: strings.ToUpper(secondEvidenceID), Start: 2, End: 3},
		{EvidenceID: strings.ToUpper(firstEvidenceID), Start: 0, End: 1},
	}
	callerPatch := &RelationshipCorrectionEntityPatch{EntityID: " " + strings.ToUpper(entityID) + " "}
	input := CorrectRelationshipInput{
		TeamID: strings.ToUpper(teamID), OwnerProfileID: strings.ToUpper(profileID), Action: " submit ",
		RelationshipID: strings.ToUpper(relationshipID), Patch: RelationshipCorrectionPatch{ObjectEntity: callerPatch},
		Supports: callerSupports,
	}

	normalized := normalizeCorrectRelationshipInput(input)

	require.Equal(t, teamID, normalized.TeamID)
	require.Equal(t, profileID, normalized.OwnerProfileID)
	require.Equal(t, relationshipID, normalized.RelationshipID)
	require.Equal(t, entityID, normalized.Patch.ObjectEntity.EntityID)
	require.ElementsMatch(t, []string{firstEvidenceID, secondEvidenceID}, []string{normalized.Supports[0].EvidenceID, normalized.Supports[1].EvidenceID})
	require.Equal(t, strings.ToUpper(secondEvidenceID), callerSupports[0].EvidenceID)
	require.Equal(t, " "+strings.ToUpper(entityID)+" ", callerPatch.EntityID)
}
