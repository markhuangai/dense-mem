package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func commitOverdueConflictResolutionWithVectors(
	t *testing.T,
	ctx context.Context,
	ledger *LedgerRepositoryImpl,
	input ApplyOverdueConflictResolutionInput,
) *ApplyOverdueConflictResolutionResult {
	t.Helper()
	plan, err := ledger.PlanRelationshipConflictResolution(ctx, RelationshipConflictResolutionInput(input))
	require.NoError(t, err)
	require.NotNil(t, plan)
	embeddings := make([]RelationshipConflictResolutionEmbedding, 0, len(plan.Documents))
	seen := make(map[string]struct{})
	for _, document := range plan.Documents {
		if _, exists := seen[document.DocumentHash]; exists {
			continue
		}
		seen[document.DocumentHash] = struct{}{}
		vector := make([]float32, plan.Fence.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		embeddings = append(embeddings, RelationshipConflictResolutionEmbedding{
			DocumentHash: document.DocumentHash,
			Embedding:    vector,
		})
	}
	result, err := ledger.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{
		Plan: *plan, Embeddings: embeddings,
	})
	require.NoError(t, err)
	return result
}

func commitConflictReviewResolutionWithVectors(
	t *testing.T,
	ctx context.Context,
	ledger *LedgerRepositoryImpl,
	resolution RelationshipConflictResolutionInput,
) *ApplyOverdueConflictResolutionResult {
	t.Helper()
	plan, err := ledger.PlanRelationshipConflictResolution(ctx, resolution)
	require.NoError(t, err)
	require.NotNil(t, plan)
	embeddings := make([]RelationshipConflictResolutionEmbedding, 0, len(plan.Documents))
	seen := make(map[string]struct{})
	for _, document := range plan.Documents {
		if _, exists := seen[document.DocumentHash]; exists {
			continue
		}
		seen[document.DocumentHash] = struct{}{}
		vector := make([]float32, plan.Fence.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		embeddings = append(embeddings, RelationshipConflictResolutionEmbedding{DocumentHash: document.DocumentHash, Embedding: vector})
	}
	result, err := ledger.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{Plan: *plan, Embeddings: embeddings})
	require.NoError(t, err)
	return result
}
