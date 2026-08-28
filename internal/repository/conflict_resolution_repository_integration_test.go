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

func TestRelationshipConflictResolutionRejectsStaleSearchGenerationBeforeLifecycleCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-resolution-stale-generation", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-resolution-stale-generation-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-generation-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-generation-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Conflict generation fence")
	firstObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "First generation value")
	secondObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Second generation value")
	first := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerA, "worker-generation-a", "generation-fence-a", "Generation fence chooses first.", subject.EntityID, firstObject.EntityID, "generation-source-a")
	second := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerB, "worker-generation-b", "generation-fence-b", "Generation fence chooses second.", subject.EntityID, secondObject.EntityID, "generation-source-b")

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_conflict_cases SET review_due_at = ?, next_review_at = ? WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	reviewed := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-generation-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, reviewed.Outcome)

	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-generation-assessment", LocalAssessmentDate: reviewNow, Model: "test-model", PolicyVersion: domain.ConflictOverduePolicyVersion})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, dossier)
	require.NotEmpty(t, dossier.Positions)
	selectedPositionID := dossier.Positions[0].PositionID
	confidence := 0.95
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{TeamID: teamID, ConflictID: conflictID, AssessmentAttemptID: reservation.AssessmentAttemptID, CaseVersion: reservation.CaseVersion, ReviewRunID: uuid.NewString(), Decision: "selected", SelectedPositionID: selectedPositionID, Confidence: &confidence, ResponseHash: "sha256:generation-fence"})
	require.NoError(t, err)
	plan, err := ledgerRepo.PlanRelationshipConflictResolution(ctx, RelationshipConflictResolutionInput{TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-generation-commit", ExpectedCaseVersion: reservation.CaseVersion, PreferredPositionID: selectedPositionID, AssessmentAttemptID: reservation.AssessmentAttemptID, Method: "ai", Now: reviewNow})
	require.NoError(t, err)
	require.False(t, plan.Stale)
	require.NotEmpty(t, plan.Documents)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE search_index_generations SET activation_state = 'deprecated' WHERE search_index_generation_id = ?::uuid`, plan.Fence.SearchIndexGenerationID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, hnsw_m, hnsw_ef_construction, query_ef_search, exact_max_rows, candidate_limit, allow_exact_fallback, activation_state, activated_at, metadata)
			SELECT gen_random_uuid(), generation + 1, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, hnsw_m, hnsw_ef_construction, query_ef_search, exact_max_rows, candidate_limit, allow_exact_fallback, 'active', now(), metadata
			FROM search_index_generations WHERE search_index_generation_id = ?::uuid
		`, plan.Fence.SearchIndexGenerationID).Error
	}))
	embeddings := conflictResolutionTestEmbeddings(plan)
	committed, err := ledgerRepo.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{Plan: *plan, Embeddings: embeddings})
	require.NoError(t, err)
	assert.True(t, committed.Stale)
	assert.False(t, committed.Resolved)

	var conflictStatus, firstStatus, secondStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM relationship_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&conflictStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, first.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&firstStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, second.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&secondStatus)
	}))
	assert.Equal(t, "overdue", conflictStatus)
	assert.Equal(t, "active", firstStatus)
	assert.Equal(t, "active", secondStatus)
}

func conflictResolutionTestEmbeddings(plan *RelationshipConflictResolutionPlan) []RelationshipConflictResolutionEmbedding {
	seen := map[string]struct{}{}
	values := make([]RelationshipConflictResolutionEmbedding, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		if _, exists := seen[document.DocumentHash]; exists {
			continue
		}
		seen[document.DocumentHash] = struct{}{}
		vector := make([]float32, plan.Fence.EmbeddingDimensions)
		vector[0] = 1
		values = append(values, RelationshipConflictResolutionEmbedding{DocumentHash: document.DocumentHash, Embedding: vector})
	}
	return values
}
