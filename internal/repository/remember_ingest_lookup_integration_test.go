package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberIngestExistsScopesTeamAndOwner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "remember-ingest-lookup-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "remember-ingest-lookup-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "remember-ingest-lookup-owner-b")
	teamB := createLedgerTeam(t, adminDB, rls, "remember-ingest-lookup-team-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamB, "remember-ingest-lookup-owner-c")
	repo := NewLedgerRepository(appDB, rls)
	const key = "dream-feedback:legacy-alias:confirm_true"
	createSemanticIngest(t, ctx, repo, teamA, ownerA, key, "legacy Dream confirmation evidence")

	exists, err := repo.RememberIngestExists(ctx, RememberIngestLookupInput{TeamID: teamA, OwnerProfileID: ownerA, IdempotencyKey: key})
	require.NoError(t, err)
	require.True(t, exists)

	for name, input := range map[string]RememberIngestLookupInput{
		"same team different owner": {TeamID: teamA, OwnerProfileID: ownerB, IdempotencyKey: key},
		"different team":            {TeamID: teamB, OwnerProfileID: ownerC, IdempotencyKey: key},
		"unknown key":               {TeamID: teamA, OwnerProfileID: ownerA, IdempotencyKey: "dream-feedback:missing:confirm_true"},
	} {
		t.Run(name, func(t *testing.T) {
			exists, err := repo.RememberIngestExists(ctx, input)
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}
