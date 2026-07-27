package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLedgerCreateIngestAllowsPerEvidenceSupersedesInSourceRevisionBatch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-source-per-evidence")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "source-per-evidence-owner")
	repo := NewLedgerRepository(appDB, rls)

	first, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{
			{
				Content:                   "First source revision fragment.",
				SourceKey:                 "doc://per-evidence",
				SourceRevisionToken:       "rev-1",
				SourceRevisionContentHash: "sha256:rev1",
			},
			{
				Content:                   "Second source revision fragment.",
				SourceKey:                 "doc://per-evidence",
				SourceRevisionToken:       "rev-1",
				SourceRevisionContentHash: "sha256:rev1",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, first.Evidence, 2)

	second, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{
			{
				Content:                       "First revised source fragment.",
				SourceKey:                     "doc://per-evidence",
				SourceRevisionToken:           "rev-2",
				ExpectedPreviousRevisionToken: "rev-1",
				SourceRevisionContentHash:     "sha256:rev2",
				SupersedesFragmentIDs:         []string{first.Evidence[0].FragmentID},
			},
			{
				Content:                       "Second revised source fragment.",
				SourceKey:                     "doc://per-evidence",
				SourceRevisionToken:           "rev-2",
				ExpectedPreviousRevisionToken: "rev-1",
				SourceRevisionContentHash:     "sha256:rev2",
				SupersedesFragmentIDs:         []string{first.Evidence[1].FragmentID},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, second.Evidence, 2)
}
