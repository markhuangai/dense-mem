package postgres

import (
	"context"
	"testing"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type dreamFixtureStore struct {
	*knowledgepostgres.Store
	*dreamStore
}

// dreamStore keeps the native Dream store explicit while allowing fixtures to
// exercise semantic writes and Dream reads through one local test seam.
type dreamStore = Store

func newDreamFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *dreamFixtureStore {
	return &dreamFixtureStore{Store: knowledgepostgres.NewStore(db, rls, knowledgepostgres.ConflictRuntimeConfig{}), dreamStore: NewStore(db, rls)}
}

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
	result, err := repo.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: sha256Hex(content), Evidence: []knowledgepostgres.EvidenceInput{{Content: content}}})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
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

func requireTestEvidenceFragment(t *testing.T, result *knowledgepostgres.EvidenceIngestResult) knowledgepostgres.EvidenceFragment {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Evidence, 1)
	return result.Evidence[0]
}
