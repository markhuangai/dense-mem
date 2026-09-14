package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/stretchr/testify/require"
)

type ApplyRelationshipDecisionInput = knowledgepostgres.ApplyRelationshipDecisionInput
type EvidenceSupportInput = knowledgepostgres.EvidenceSupportInput

func createSemanticEntity(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, kind, name string) *knowledgepostgres.EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, knowledgepostgres.CreateEntityInput{TeamID: teamID, OwnerProfileID: ownerID, EntityKind: kind, CanonicalName: name})
	require.NoError(t, err)
	return entity
}
func createSemanticIngest(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, key, content string) *knowledgepostgres.EvidenceIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: sha256Hex(content), Evidence: []knowledgepostgres.EvidenceInput{{Content: content}}})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func applySemanticDecision(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, input knowledgepostgres.ApplyRelationshipDecisionInput) *knowledgepostgres.RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}
