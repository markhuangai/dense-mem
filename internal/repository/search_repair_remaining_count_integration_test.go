package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSearchRepairApplyReportsExactRemainingDriftCount(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-remaining-count-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-remaining-count-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-remaining-count", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	for index := 0; index < 3; index++ {
		content := fmt.Sprintf("remaining drift count evidence %d", index)
		createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID,
			IdempotencyKey: fmt.Sprintf("search-repair-remaining-count-%d", index),
			RequestHash:    sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
		})
	}
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "remaining-count-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, selected, 1)

	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.25, 0.75}}},
	})

	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.EqualValues(t, 2, apply.RemainingDriftedCount)
}
