package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSynchronousRememberCommitReturnsSupersededEvidenceIDs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-supersession-result", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-supersession-result-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-supersession-result-owner")
	repo := NewLedgerRepository(appDB, rls)

	targetContent := "Original synchronous supersession target."
	target, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-supersession-result-target", RequestHash: "sync-supersession-result-target-hash",
		Evidence: []EvidenceInput{{Content: targetContent, ContentHash: sha256Hex(targetContent)}},
	})
	require.NoError(t, err)
	require.Len(t, target.Evidence, 1)

	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-supersession-result", "sync-supersession-result-hash", nil)
	input.CreateIngest.Evidence[0].SupersedesEvidenceIDs = []string{target.Evidence[0].FragmentID}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)

	committed, err := repo.CommitSynchronousRemember(ctx, input)

	require.NoError(t, err)
	require.NotNil(t, committed.Ingest)
	require.Len(t, committed.Ingest.Evidence, 2)
	require.Equal(t, []string{target.Evidence[0].FragmentID}, committed.Ingest.Evidence[0].SupersededEvidenceIDs)
}
