//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type CreateIngestResult = EvidenceIngestResult

func createSemanticIngest(
	t *testing.T,
	ctx context.Context,
	repo *Store,
	teamID string,
	ownerID string,
	idempotencyKey string,
	content string,
) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, CreateIngestInput{
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

func createTestIngest(ctx context.Context, repo *Store, input CreateIngestInput) (*EvidenceIngestResult, error) {
	return repo.CreateIngestForTest(ctx, input)
}
