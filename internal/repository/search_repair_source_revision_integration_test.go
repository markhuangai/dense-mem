package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchRepairExcludesSupersededSourceRevision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-current-revision-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-current-revision-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-current-revision", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	const sourceKey = "doc://search-repair-current-revision"
	first := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-current-revision-first",
		RequestHash: sha256Hex("search repair source revision one"), Evidence: []EvidenceInput{{
			Content: "search repair source revision one", SourceType: "document", SourceKey: sourceKey,
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:search-repair-source-rev-1",
		}},
	})
	second := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-current-revision-second",
		RequestHash: sha256Hex("search repair source revision two"), Evidence: []EvidenceInput{{
			Content: "search repair source revision two", SourceType: "document", SourceKey: sourceKey,
			SourceRevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash: "sha256:search-repair-source-rev-2",
		}},
	})
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, projection.ExpectedDocuments)
	require.EqualValues(t, 2, projection.DriftedDocuments)
	selected, hasMore, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 2,
	})
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, selected, 2)
	for _, item := range selected {
		switch item.SourceID {
		case first.Evidence[0].FragmentID:
			require.True(t, item.Retired, "superseded source document must be retired")
		case second.Evidence[0].FragmentID:
			require.False(t, item.Retired)
		default:
			t.Fatalf("unexpected repair source %s", item.SourceID)
		}
	}
}
