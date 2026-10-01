//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityOwnershipPreservesPublishedIdentityAndMetadata(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	encoded, err := json.Marshal(f.publication)
	require.NoError(t, err)
	var input CommunitySnapshotPublishInput
	require.NoError(t, json.Unmarshal(encoded, &input))
	run, err := f.store.ClaimCommunityRun(ctx, CommunityRunClaimInput{TeamID: f.teamID, WindowKey: "supplied-publication", SourceFingerprint: "fixture-sources"})
	require.NoError(t, err)
	input.RunID = run.RunID
	input.AlgorithmKind = " custom-publication-kind "
	input.AlgorithmVersion = " custom-publication-version "
	input.ProfileVersion = " custom-publication-profile "
	input.ConfigurationHash = " custom-publication-config "
	for i := range input.Communities {
		input.Communities[i].CommunityID = communityOwnershipID(100 + i)
		input.Communities[i].LogicalCommunityID = " " + communityOwnershipID(200+i) + " "
		input.Communities[i].SummaryVersion = " custom-summary-version "
	}
	require.NoError(t, f.store.PublishCommunitySnapshot(ctx, input))
	for i := range input.Communities {
		record, err := f.store.GetCommunity(ctx, CommunityGetInput{TeamID: f.teamID, CommunityID: communityOwnershipID(100 + i)})
		require.NoError(t, err)
		require.Equal(t, communityOwnershipID(200+i), record.LogicalCommunityID)
		require.NotEqual(t, record.CommunityID, record.LogicalCommunityID)
		require.Equal(t, "custom-summary-version", record.SummaryVersion)
		require.Equal(t, "current", record.Status)
	}
	latest, err := f.store.LatestCommunityRun(ctx, f.teamID)
	require.NoError(t, err)
	require.Equal(t, "completed", latest.Status)
	require.Equal(t, "custom-publication-kind", latest.AlgorithmKind)
	require.Equal(t, "custom-publication-version", latest.AlgorithmVersion)
	require.Equal(t, "custom-publication-profile", latest.ProfileVersion)
	require.Equal(t, "custom-publication-config", latest.ConfigurationHash)
	require.Equal(t, "fixture-sources", latest.SourceFingerprint)
	t.Log("case=successful_publication logical_identity=distinct metadata=custom-preserved status=completed")
}

func TestCommunityOwnershipExcludesOldGenerationRecall(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	input := f.mixedInput()
	before, err := f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	require.Len(t, before, 10)
	for _, record := range before {
		require.NotEmpty(t, record.Relationships)
	}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND kind='team_shared'`, f.teamID).Error
	}))
	var currentCount int
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM community_records WHERE team_id=?::uuid AND status='current'`, f.teamID).Row().Scan(&currentCount)
	}))
	require.Equal(t, 12, currentCount)
	after, err := f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	require.Empty(t, after)
	t.Logf("case=fresh_generation_change before=%s after=[] status_current=12", f.signature(before))
}
