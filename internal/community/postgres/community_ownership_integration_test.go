//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityOwnershipPreservesRecallOrderBoundsAndCoverage(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	mixed := []int{0, 1, 2, 4, 3, 7, 5, 6, 8, 9}
	fallback := []int{7, 5, 6, 8, 9, 10, 11, 4, 3, 1}
	cases := []struct {
		name  string
		input CommunityRecallInput
		want  []int
	}{
		{"mixed_all_lanes", f.mixedInput(), mixed},
		{"remaining_default", CommunityRecallInput{TeamID: f.teamID}, fallback[:3]},
		{"remaining_maximum", CommunityRecallInput{TeamID: f.teamID, Limit: 100}, fallback},
		{"returned_evidence", CommunityRecallInput{TeamID: f.teamID, ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}}, []int{0, 7, 5}},
		{"known_evidence", CommunityRecallInput{TeamID: f.teamID, KnownEvidenceIDs: []string{f.evidenceIDs[1]}}, []int{1, 7, 5}},
		{"known_relationship", CommunityRecallInput{TeamID: f.teamID, KnownRelationshipIDs: []string{f.sources[2].RelationshipID}}, []int{2, 7, 5}},
		{"seed_relationship", CommunityRecallInput{TeamID: f.teamID, SeedRelationshipIDs: []string{f.sources[3].RelationshipID}}, []int{3, 7, 5}},
		{"expand_entity", CommunityRecallInput{TeamID: f.teamID, ExpandFromEntityIDs: []string{f.expandID}}, []int{4, 7, 5}},
		{"no_match", CommunityRecallInput{TeamID: f.teamID, Query: "absent"}, []int{}},
		{"other_team", CommunityRecallInput{TeamID: f.otherTeam, ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}}, []int{}},
	}
	overlapping := f.mixedInput()
	overlapping.KnownRelationshipIDs = append(overlapping.KnownRelationshipIDs, f.sources[0].RelationshipID)
	overlapping.SeedRelationshipIDs = append(overlapping.SeedRelationshipIDs, f.sources[0].RelationshipID, f.sources[1].RelationshipID)
	cases = append(cases, struct {
		name  string
		input CommunityRecallInput
		want  []int
	}{"overlapping_lanes", overlapping, mixed})

	for _, limit := range []int{-1, 0, 1, 3, 10, 100} {
		input := f.mixedInput()
		input.Limit = limit
		size := limit
		if size <= 0 {
			size = 3
		}
		if size > 10 {
			size = 10
		}
		cases = append(cases, struct {
			name  string
			input CommunityRecallInput
			want  []int
		}{fmt.Sprintf("community_limit_%d", limit), input, mixed[:size]})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.counters.reset()
			records, err := f.store.RecallCommunities(ctx, tc.input)
			require.NoError(t, err)
			expected := make([]string, 0, len(tc.want))
			for _, i := range tc.want {
				expected = append(expected, communityOwnershipID(i))
			}
			require.Equal(t, expected, communityOwnershipIDs(records))
			require.EqualValues(t, 1, f.counters.transactions.Load())
			require.EqualValues(t, 1, f.counters.completions.Load())
			for index, record := range records {
				require.Equal(t, index+1, record.Rank)
				require.Equal(t, record.CommunityID, record.LogicalCommunityID)
				require.Len(t, record.TopEntities, 5)
				require.Len(t, record.Relationships, 5)
				require.True(t, record.RelationshipsTruncated)
				for _, hit := range record.Relationships {
					require.NotContains(t, tc.input.KnownRelationshipIDs, hit.RelationshipID)
				}
			}
			t.Logf("case=%s signature=%s sql=%d transactions=%d completions=%d providers=0", tc.name, f.signature(records), f.counters.statements.Load(), f.counters.transactions.Load(), f.counters.completions.Load())
		})
	}
	for _, limit := range []int{-1, 0, 1, 5, 20, 100} {
		input := f.mixedInput()
		input.RelationshipLimit = limit
		records, err := f.store.RecallCommunities(ctx, input)
		require.NoError(t, err)
		count := limit
		if count <= 0 {
			count = 5
		}
		if count > 20 {
			count = 20
		}
		for _, record := range records {
			require.Len(t, record.Relationships, count)
			require.True(t, record.RelationshipsTruncated)
		}
		t.Logf("case=relationship_limit_%d signature=%s", limit, f.signature(records))
	}
	input := f.mixedInput()
	input.CoveredGroupKeys = []string{f.sources[4].SemanticGroupKey}
	input.ExcludedGroupKeys = []string{f.sources[5].SemanticGroupKey}
	records, err := f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	for _, record := range records {
		for _, hit := range record.Relationships {
			require.NotEqual(t, f.sources[4].RelationshipID, hit.RelationshipID)
			require.NotEqual(t, f.sources[5].RelationshipID, hit.RelationshipID)
		}
	}
	t.Logf("case=covered_excluded signature=%s", f.signature(records))
	boundary := CommunityRecallInput{TeamID: f.teamID, ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}, Limit: 10}
	for _, source := range f.sources[9:] {
		boundary.CoveredGroupKeys = append(boundary.CoveredGroupKeys, source.SemanticGroupKey)
	}
	bounded, err := f.store.RecallCommunities(ctx, boundary)
	require.NoError(t, err)
	for _, record := range bounded {
		require.Len(t, record.Relationships, 5)
		require.Equal(t, record.CommunityID == communityOwnershipID(0) || record.CommunityID == communityOwnershipID(1) || record.CommunityID == communityOwnershipID(2) || record.CommunityID == communityOwnershipID(3), record.RelationshipsTruncated)
	}
	t.Logf("case=exact_truncation_boundary signature=%s", f.signature(bounded))

	input.CoveredGroupKeys = nil
	input.ExcludedGroupKeys = nil
	for _, source := range f.sources {
		input.CoveredGroupKeys = append(input.CoveredGroupKeys, source.SemanticGroupKey)
	}
	records, err = f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	require.Empty(t, records)
	groups, err := f.store.ListCommunitySemanticGroups(ctx, CommunityCoverageInput{TeamID: f.teamID, EvidenceIDs: []string{f.evidenceIDs[0]}, RelationshipIDs: []string{f.sources[2].RelationshipID}})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{f.sources[0].SemanticGroupKey, f.sources[2].SemanticGroupKey}, groups)
	t.Log("case=all_covered signature=[] coverage=returned_and_known")
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE community_sources SET source_rank=1 WHERE team_id=?::uuid AND community_id=?::uuid AND relationship_id IN (?::uuid,?::uuid)`, f.teamID, communityOwnershipID(0), f.sources[4].RelationshipID, f.sources[5].RelationshipID).Error
	}))
	tied, err := f.store.RecallCommunities(ctx, CommunityRecallInput{TeamID: f.teamID, ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}, Limit: 1})
	require.NoError(t, err)
	require.Len(t, tied, 1)
	tieIDs := []string{f.sources[4].RelationshipID, f.sources[5].RelationshipID}
	sort.Strings(tieIDs)
	require.Equal(t, f.sources[0].RelationshipID, tied[0].Relationships[0].RelationshipID)
	require.Equal(t, tieIDs, []string{tied[0].Relationships[1].RelationshipID, tied[0].Relationships[2].RelationshipID})
	t.Log("case=relationship_source_rank_uuid_tie order=ascending")

}

func TestCommunityOwnershipRejectsPublicationAndRollsBack(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	copyPublication := func() CommunitySnapshotPublishInput {
		encoded, err := json.Marshal(f.publication)
		require.NoError(t, err)
		var input CommunitySnapshotPublishInput
		require.NoError(t, json.Unmarshal(encoded, &input))
		return input
	}
	for _, tc := range []struct {
		name, prefix string
		change       func(*CommunitySnapshotPublishInput)
	}{
		{"team", "team_id is required:", func(i *CommunitySnapshotPublishInput) { i.TeamID = "invalid" }},
		{"run", "run_id is required:", func(i *CommunitySnapshotPublishInput) { i.RunID = "invalid" }},
		{"fingerprint", "source_fingerprint is required", func(i *CommunitySnapshotPublishInput) { i.SourceFingerprint = " " }},
		{"counts", "node and edge counts must be non-negative", func(i *CommunitySnapshotPublishInput) { i.NodeCount = -1 }},
		{"summary", "community summary is required", func(i *CommunitySnapshotPublishInput) { i.Communities[0].Summary = " " }},
		{"community_id", "community_id is required:", func(i *CommunitySnapshotPublishInput) { i.Communities[0].CommunityID = "invalid" }},
		{"member_score", "membership_score must be between zero and one", func(i *CommunitySnapshotPublishInput) { i.Communities[0].Memberships[0].MembershipScore = 2 }},
		{"source_version", "source relationship_version must be greater than zero", func(i *CommunitySnapshotPublishInput) { i.Communities[0].Sources[0].RelationshipVersion = 0 }},
	} {
		input := copyPublication()
		tc.change(&input)
		f.counters.reset()
		err := f.store.PublishCommunitySnapshot(ctx, input)
		require.ErrorContains(t, err, tc.prefix)
		require.Zero(t, f.counters.statements.Load())
		require.Zero(t, f.counters.transactions.Load())
		t.Logf("case=publication_%s error=%s sql=0 transactions=0 providers=0", tc.name, tc.prefix)
	}
	input := copyPublication()
	input.Communities[0].Sources[0].RelationshipVersion++
	require.ErrorIs(t, f.store.PublishCommunitySnapshot(ctx, input), ErrCommunitySourceStale)
	lease := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	run, err := f.store.ClaimCommunityRun(ctx, CommunityRunClaimInput{TeamID: f.teamID, WindowKey: "rollback", LeaseUntil: lease,
		AlgorithmKind: "custom", AlgorithmVersion: "custom-v", ProfileVersion: "custom-p", ConfigurationHash: "custom-c", SourceFingerprint: "fixture-sources"})
	require.NoError(t, err)
	require.True(t, run.Claimed)
	require.Equal(t, "custom", run.AlgorithmKind)
	require.Equal(t, "custom-v", run.AlgorithmVersion)
	require.Equal(t, "custom-p", run.ProfileVersion)
	require.Equal(t, "custom-c", run.ConfigurationHash)
	var storedLease time.Time
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT lease_until FROM community_snapshot_runs WHERE team_id=?::uuid AND run_id=?::uuid`, f.teamID, run.RunID).Row().Scan(&storedLease)
	}))
	require.True(t, lease.Equal(storedLease))
	input = copyPublication()
	input.RunID = run.RunID
	require.Error(t, f.store.PublishCommunitySnapshot(ctx, input))
	current, err := f.store.ListCommunities(ctx, CommunityListInput{TeamID: f.teamID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, current, 12)
	superseded, err := f.store.ListCommunities(ctx, CommunityListInput{TeamID: f.teamID, Status: "superseded"})
	require.NoError(t, err)
	require.Empty(t, superseded)
	latest, err := f.store.LatestCommunityRun(ctx, f.teamID)
	require.NoError(t, err)
	require.Equal(t, "running", latest.Status)
	for _, status := range []string{"failed", "too_large", "cancelled", "skipped"} {
		run, err := f.store.ClaimCommunityRun(ctx, CommunityRunClaimInput{TeamID: f.teamID, WindowKey: "status-" + status, SourceFingerprint: "fixture-sources"})
		require.NoError(t, err)
		require.NoError(t, f.store.CompleteCommunityRun(ctx, CommunityRunCompleteInput{TeamID: f.teamID, RunID: run.RunID, Status: status, Error: strings.Repeat("é", 600)}))
		latest, err := f.store.LatestCommunityRun(ctx, f.teamID)
		require.NoError(t, err)
		require.Equal(t, status, latest.Status)
		require.Len(t, []rune(latest.Error), 512)
	}
	t.Log("case=publication_rollback current=12 superseded=0 stale=ErrCommunitySourceStale custom_metadata=preserved terminal_statuses=preserved")
}

func TestCommunityOwnershipExcludesPrivateAndStaleSnapshots(t *testing.T) {
	f := newCommunityOwnershipFixture(t)
	ctx := context.Background()
	private, err := privacypostgres.NewMemorySpaceRepository(f.adminDB, f.rls).EnsureProfilePrivate(ctx, uuid.MustParse(f.teamID), uuid.MustParse(f.ownerB))
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE community_records SET space_id=?::uuid,space_generation=? WHERE team_id=?::uuid AND community_id=?::uuid`, private.ID, private.Generation, f.teamID, communityOwnershipID(0)).Error
	}))
	input := f.mixedInput()
	records, err := f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	require.NotContains(t, communityOwnershipIDs(records), communityOwnershipID(0))
	t.Logf("case=private_snapshot signature=%s", f.signature(records))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_records SET version=version+1 WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.teamID, f.sources[4].RelationshipID).Error
	}))
	records, err = f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	for _, record := range records {
		for _, hit := range record.Relationships {
			require.NotEqual(t, f.sources[4].RelationshipID, hit.RelationshipID)
		}
	}
	stale, err := f.store.RefreshCommunityStaleness(ctx, CommunityStalenessInput{TeamID: f.teamID})
	require.NoError(t, err)
	require.Equal(t, 11, stale)
	records, err = f.store.RecallCommunities(ctx, input)
	require.NoError(t, err)
	require.Empty(t, records)
	t.Log("case=source_version_stale stale=11 signature=[]")
}
