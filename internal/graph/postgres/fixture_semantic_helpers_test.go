package postgres

import (
	"context"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/stretchr/testify/require"
	"testing"
)

func createSemanticEntity(t *testing.T, ctx context.Context, repo interface {
	CreateEntity(context.Context, knowledgepostgres.CreateEntityInput) (*knowledgepostgres.EntityRecord, error)
}, teamID, ownerID, kind, name string) *knowledgepostgres.EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, knowledgepostgres.CreateEntityInput{TeamID: teamID, OwnerProfileID: ownerID, EntityKind: kind, CanonicalName: name})
	require.NoError(t, err)
	return entity
}
func createSemanticIngest(t *testing.T, ctx context.Context, repo interface {
	CreateIngestForTest(context.Context, knowledgepostgres.CreateIngestInput) (*knowledgepostgres.EvidenceIngestResult, error)
}, teamID, ownerID, key, content string) *knowledgepostgres.EvidenceIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: key, Evidence: []knowledgepostgres.EvidenceInput{{Content: content}}})
	require.NoError(t, err)
	return result
}
func applySemanticDecision(t *testing.T, ctx context.Context, repo interface {
	ApplyRelationshipDecision(context.Context, knowledgepostgres.ApplyRelationshipDecisionInput) (*knowledgepostgres.RelationshipDecisionResult, error)
}, input knowledgepostgres.ApplyRelationshipDecisionInput) *knowledgepostgres.RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}
