package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedgerCreateIngestAdvancesWholeSourceRevisionWithoutEvidenceTargetList(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-source-revision-whole")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "source-revision-owner")
	repo := NewLedgerRepository(appDB, rls)

	first, err := repo.CreateIngest(ctx, CreateIngestInput{
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

	second, err := repo.CreateIngest(ctx, CreateIngestInput{
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

	_, err = repo.CreateIngest(ctx, CreateIngestInput{
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

func TestRememberRejectsMismatchedProvenanceWhenReusingSourceRevision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-source-reuse-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-source-reuse-owner")
	ledger := NewLedgerRepository(appDB, rls)
	const (
		sourceKey     = "doc://remember-source-reuse"
		revisionToken = "rev-1"
		content       = "The stored source revision has immutable provenance."
	)
	contentHash := sha256Hex(content)
	baseEvidence := func() EvidenceInput {
		return EvidenceInput{
			Content: content, SourceType: "document", Authority: "primary",
			SourceKey: sourceKey, SourceRevisionToken: revisionToken,
			SourceRevisionContentHash: contentHash,
			SourceRevisionEnvelope: map[string]any{
				"source_type": "document", "source": "wiki", "source_group": "docs",
				"metadata": map[string]any{"section": "remember"},
			},
		}
	}
	seed, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		IdempotencyKey: "remember-source-reuse-seed", RequestHash: "remember-source-reuse-seed-hash",
		Evidence: []EvidenceInput{baseEvidence()},
	})
	require.NoError(t, err)
	require.Len(t, seed.Evidence, 1)

	matching := baseEvidence()
	replayed, err := ledger.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: matching.SourceKey,
		SourceKind: sourceKindForEvidence(matching.SourceType), Authority: matching.Authority,
		RevisionToken:                 matching.SourceRevisionToken,
		ExpectedPreviousRevisionToken: matching.ExpectedPreviousRevisionToken,
		ContentHash:                   matching.SourceRevisionContentHash, Envelope: matching.SourceRevisionEnvelope,
	})
	require.NoError(t, err)
	assert.Equal(t, seed.Evidence[0].SourceRevisionID, replayed.SourceRevisionID)
	matchingStage, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, TelemetryRemember: true,
		IdempotencyKey: "remember-source-reuse-match", RequestHash: "remember-source-reuse-match-hash",
		Evidence: []EvidenceInput{matching},
	})
	require.NoError(t, err)
	require.NotNil(t, matchingStage)

	tests := []struct {
		name   string
		key    string
		mutate func(*EvidenceInput)
	}{
		{name: "previous revision", key: "previous", mutate: func(item *EvidenceInput) {
			item.ExpectedPreviousRevisionToken = "rev-0"
		}},
		{name: "source kind", key: "kind", mutate: func(item *EvidenceInput) {
			item.SourceType = "manual"
		}},
		{name: "authority", key: "authority", mutate: func(item *EvidenceInput) {
			item.Authority = "secondary"
		}},
		{name: "source fields", key: "source", mutate: func(item *EvidenceInput) {
			item.SourceRevisionEnvelope["source"] = "other-wiki"
		}},
		{name: "metadata", key: "metadata", mutate: func(item *EvidenceInput) {
			item.SourceRevisionEnvelope["metadata"] = map[string]any{"section": "other"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := baseEvidence()
			test.mutate(&item)
			staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
				TeamID: teamID, OwnerProfileID: ownerID, TelemetryRemember: true,
				IdempotencyKey: "remember-source-reuse-" + test.key,
				RequestHash:    "remember-source-reuse-hash-" + test.key,
				Evidence:       []EvidenceInput{item},
			})
			require.Nil(t, staged)
			var preflight *RememberPreflightError
			require.ErrorAs(t, err, &preflight)
			require.Contains(t, preflight.Issues, RememberPreflightIssue{
				Path: "/evidence/0/source_revision", Code: "conflict",
				Message: "source_revision already exists with different provenance",
			})

			_, err = ledger.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
				TeamID: teamID, OwnerProfileID: ownerID, SourceKey: item.SourceKey,
				SourceKind: sourceKindForEvidence(item.SourceType), Authority: item.Authority,
				RevisionToken:                 item.SourceRevisionToken,
				ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
				ContentHash:                   item.SourceRevisionContentHash, Envelope: item.SourceRevisionEnvelope,
			})
			require.ErrorIs(t, err, ErrSourceRevisionConflict)
		})
	}
}
