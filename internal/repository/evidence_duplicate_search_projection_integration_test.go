package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRememberDuplicateExactReuseRequiresCurrentSearchProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-exact-search-fence", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-exact-search-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-exact-search-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	content := "exact evidence with a current search projection"
	canonical := duplicateRememberInput(teamID, ownerID, "duplicate-exact-search-fence-canonical", content, false)
	commitDuplicateFixture(t, ctx, repo, canonical)
	require.EqualValues(t, 1, duplicateCount(t, adminDB, rls, `
		SELECT count(*)
		FROM search_documents
		WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id = ?::uuid
		  AND search_state = 'current' AND embedding IS NOT NULL
	`, teamID, canonical.Evidence[0].FragmentID))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding = NULL, embedding_error = 'test projection failure'
			WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id = ?::uuid
		`, teamID, canonical.Evidence[0].FragmentID).Error
	}))

	submitted := duplicateRememberInput(teamID, ownerID, "duplicate-exact-search-fence-submitted", content, false)
	plan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateCandidateInput(submitted))
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1, "a failed canonical projection must require a fresh duplicate assessment vector")
	resolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateCandidateInput(submitted), duplicatePlanEmbeddings(plan))
	require.NoError(t, err)
	require.Len(t, resolved.Exact, 1)
	require.False(t, resolved.Exact[0].Exact)
	require.Equal(t, "new", resolved.Exact[0].Disposition)
	require.Len(t, resolved.Candidates, 1)
	require.Empty(t, resolved.Candidates[0].Candidates, "failed projections must not be semantic candidates")
}
