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

func TestConflictResolutionRefreshesPreviousProjectionGenerationAfterEmbedding(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-resolution-projection-refresh", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-resolution-projection-refresh-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-projection-refresh-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Projection refresh subject")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Projection refresh project")
	relationship := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerID, "worker-projection-refresh",
		"projection-refresh-relationship", "The subject works on the projection refresh project.", subject.EntityID, object.EntityID, "source-group-projection-refresh",
	).RelationshipResults[0].Relationship
	require.NotNil(t, relationship)

	projectionGenerationID := uuid.NewString()
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, last_error
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1, 2,
			    'failed', 1, 1, 0, 1, 'conflict projection pending refresh'
			)
		`, teamID, projectionGenerationID).Error
	}))

	var documentText string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		var err error
		documentText, err = placementRelationshipSearchText(ctx, tx, relationship)
		return err
	}))
	documentInput := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{DocumentText: documentText})
	queued, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: relationship.RelationshipID,
		SourceVersion: int64(relationship.Version), ProjectionFormat: 2, ProjectionGenerationID: projectionGenerationID,
		DocumentText: documentInput.DocumentText, SpaceID: relationship.SpaceID, SpaceGeneration: relationship.SpaceGeneration,
	})
	require.NoError(t, err)
	require.NotEmpty(t, queued.QueuedJobID)

	var contract *ActiveSearchContract
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		var err error
		contract, err = loadActiveSearchContractInTx(ctx, tx)
		return err
	}))
	require.NotNil(t, contract)
	document := RelationshipConflictResolutionDocument{
		TeamID: relationship.TeamID, RelationshipID: relationship.RelationshipID, OwnerProfileID: relationship.OwnerProfileID,
		SpaceID: relationship.SpaceID, SpaceGeneration: relationship.SpaceGeneration, SourceVersion: int64(relationship.Version),
		DocumentHash: documentInput.DocumentHash, DocumentText: documentInput.DocumentText,
	}
	fence := RelationshipConflictResolutionFence{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		EmbeddingModel: contract.EmbeddingModel, SearchIndexGenerationID: contract.SearchIndexGenerationID,
		IndexGeneration: contract.IndexGeneration,
	}
	vector := make([]float32, contract.EmbeddingDimensions)
	vector[0] = 1
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return applyConflictResolutionEmbedding(ctx, tx, document, fence, vector)
	}))

	var projectionID, searchState, jobState, generationState string
	var hasEmbedding bool
	var projectedCount, currentVectorCount, failedJobCount int64
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COALESCE(projection_generation_id::text, ''), search_state, embedding IS NOT NULL
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, queued.SearchDocumentID).Row().Scan(&projectionID, &searchState, &hasEmbedding); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, queued.QueuedJobID).Row().Scan(&jobState); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT state, projected_count, current_vector_count, failed_job_count
			FROM search_projection_generations
			WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid
		`, teamID, projectionGenerationID).Row().Scan(&generationState, &projectedCount, &currentVectorCount, &failedJobCount)
	}))
	assert.Empty(t, projectionID)
	assert.Equal(t, string(domain.SearchProjectionCurrent), searchState)
	assert.True(t, hasEmbedding)
	assert.Equal(t, "stale", jobState)
	assert.Equal(t, "current", generationState)
	assert.Zero(t, projectedCount)
	assert.Zero(t, currentVectorCount)
	assert.Zero(t, failedJobCount)
}

func TestConflictResolutionRejectsIncompleteEmbeddingBatchWithoutWrites(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	fixture := setupPrivateSpaceConflictResolutionFixture(t, ctx, adminDB, appDB, rls, "conflict-resolution-incomplete-batch")
	plan, err := fixture.ledger.PlanRelationshipConflictResolution(ctx, fixture.resolution)
	require.NoError(t, err)
	require.False(t, plan.Stale)
	require.NotEmpty(t, plan.Documents)
	relationshipIDs := make([]string, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		relationshipIDs = append(relationshipIDs, document.RelationshipID)
	}
	before := captureConflictResolutionStateSnapshot(t, ctx, adminDB, rls, fixture.teamID, fixture.conflictID, relationshipIDs)
	_, err = fixture.ledger.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{Plan: *plan})
	require.ErrorContains(t, err, "conflict resolution embeddings are incomplete")
	after := captureConflictResolutionStateSnapshot(t, ctx, adminDB, rls, fixture.teamID, fixture.conflictID, relationshipIDs)
	assert.Equal(t, before, after)
}
