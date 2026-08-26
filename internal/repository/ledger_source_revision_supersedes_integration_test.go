package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLedgerSourceRevisionAdvancesWholeBatch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-source-revision-whole")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "source-revision-owner")
	repo := NewLedgerRepository(appDB, rls)

	first, err := createTestIngest(ctx, repo, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{
			{
				Content:                   "First source revision evidence.",
				SourceKey:                 "doc://whole-revision",
				SourceRevisionToken:       "rev-1",
				SourceRevisionContentHash: "sha256:rev1",
			},
			{
				Content:                   "Second source revision evidence.",
				SourceKey:                 "doc://whole-revision",
				SourceRevisionToken:       "rev-1",
				SourceRevisionContentHash: "sha256:rev1",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, first.Evidence, 2)

	second, err := createTestIngest(ctx, repo, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{
			{
				Content:                       "First revised source evidence.",
				SourceKey:                     "doc://whole-revision",
				SourceRevisionToken:           "rev-2",
				ExpectedPreviousRevisionToken: "rev-1",
				SourceRevisionContentHash:     "sha256:rev2",
			},
			{
				Content:                       "Second revised source evidence.",
				SourceKey:                     "doc://whole-revision",
				SourceRevisionToken:           "rev-2",
				ExpectedPreviousRevisionToken: "rev-1",
				SourceRevisionContentHash:     "sha256:rev2",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, second.Evidence, 2)

	_, err = createTestIngest(ctx, repo, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{{
			Content:                       "Invalid source revision advancement.",
			SourceKey:                     "doc://whole-revision",
			SourceRevisionToken:           "rev-3",
			ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash:     "sha256:rev3",
		}},
	})
	require.ErrorIs(t, err, ErrSourceRevisionConflict)
}
