//go:build integration

package postgres

import (
	"context"
	"testing"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/stretchr/testify/require"
)

func createSemanticIngest(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, key, content string) *knowledgepostgres.EvidenceIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: sha256Hex(content), Evidence: []knowledgepostgres.EvidenceInput{{Content: content}}})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}
