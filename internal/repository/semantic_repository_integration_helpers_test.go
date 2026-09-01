package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type CreateIngestResult = EvidenceIngestResult

func correctRelationshipWithTestEmbeddings(ctx context.Context, semantic *SemanticRepositoryImpl, input CorrectRelationshipInput) (*CorrectRelationshipResult, error) {
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	if err != nil {
		return nil, err
	}
	return semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
}

func relationshipCorrectionTestEmbeddings(plan *RelationshipCorrectionEmbeddingPlan) []RelationshipCorrectionEmbedding {
	embeddings := make([]RelationshipCorrectionEmbedding, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		embeddings = append(embeddings, RelationshipCorrectionEmbedding{
			DocumentHash:            document.DocumentHash,
			Embedding:               make([]float32, plan.EmbeddingDimensions),
			EmbeddingContractID:     plan.EmbeddingContractID,
			EmbeddingDimensions:     plan.EmbeddingDimensions,
			EmbeddingModel:          plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration:         plan.IndexGeneration,
		})
	}
	return embeddings
}

func assertGraphHasNode(t *testing.T, nodes []SemanticGraphNode, nodeType, id, title string) {
	t.Helper()
	for _, node := range nodes {
		if node.Type == nodeType && node.ID == id {
			assert.Equal(t, title, node.Title)
			return
		}
	}
	t.Fatalf("missing %s node %s in %+v", nodeType, id, nodes)
}

func semanticGraphNodeIDs(nodes []SemanticGraphNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func semanticGraphEdgeIDs(edges []SemanticGraphEdge) []string {
	ids := make([]string, 0, len(edges))
	for _, edge := range edges {
		ids = append(ids, edge.ID)
	}
	return ids
}

func createSemanticEntity(
	t *testing.T,
	ctx context.Context,
	repo *SemanticRepositoryImpl,
	teamID string,
	ownerID string,
	kind string,
	name string,
) *EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, CreateEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityKind:     kind,
		CanonicalName:  name,
	})
	require.NoError(t, err)
	return entity
}

func createSemanticIngest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	idempotencyKey string,
	content string,
) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    sha256Hex(content),
		Evidence: []EvidenceInput{{
			Content: content,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func createTestIngest(ctx context.Context, repo *LedgerRepositoryImpl, input CreateIngestInput) (*EvidenceIngestResult, error) {
	return repo.CreateIngest(ctx, input)
}

func applySemanticDecision(
	t *testing.T,
	ctx context.Context,
	repo *SemanticRepositoryImpl,
	input ApplyRelationshipDecisionInput,
) *RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}

func assertSameTeamCanReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	profileID string,
	relationshipID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, relationshipID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func assertCrossTeamCannotReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	targetTeamID string,
	readerTeamID string,
	readerProfileID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, readerTeamID, readerProfileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
		`, targetTeamID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
