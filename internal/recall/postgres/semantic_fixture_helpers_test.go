//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

type CreateIngestResult = knowledgepostgres.EvidenceIngestResult

func createSemanticEntity(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, kind, name string) *knowledgepostgres.EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, knowledgepostgres.CreateEntityInput{TeamID: teamID, OwnerProfileID: ownerID, EntityKind: kind, CanonicalName: name})
	require.NoError(t, err)
	return entity
}

func createSemanticIngest(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, idempotencyKey, content string) *knowledgepostgres.EvidenceIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: idempotencyKey, RequestHash: sha256Hex(content), Evidence: []knowledgepostgres.EvidenceInput{{Content: content}}})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func createTestIngest(ctx context.Context, repo *knowledgepostgres.Store, input knowledgepostgres.CreateIngestInput) (*knowledgepostgres.EvidenceIngestResult, error) {
	return repo.CreateIngestForTest(ctx, input)
}

func applySemanticDecision(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, input knowledgepostgres.ApplyRelationshipDecisionInput) *knowledgepostgres.RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}
