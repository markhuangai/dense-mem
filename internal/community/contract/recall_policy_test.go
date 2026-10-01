package contract

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommunityClaimClockAndSuppliedMetadata(t *testing.T) {
	teamID, _, _, _, _, _, _ := communityPolicyTestIDs()
	now := time.Date(2026, 10, 1, 23, 59, 0, 0, time.FixedZone("fixture", 3600))
	defaults := NormalizeCommunityRunClaimInput(CommunityRunClaimInput{TeamID: teamID}, now)
	require.Equal(t, "2026-10-01", defaults.WindowKey)
	require.Equal(t, now.UTC().Add(30*time.Second), defaults.LeaseUntil)
	require.Equal(t, CommunityAlgorithmKind, defaults.AlgorithmKind)
	require.Equal(t, CommunityAlgorithmVersion, defaults.AlgorithmVersion)
	require.Equal(t, CommunityProfileVersion, defaults.ProfileVersion)
	supplied := CommunityRunClaimInput{TeamID: teamID, WindowKey: " explicit ", AlgorithmKind: " custom ",
		AlgorithmVersion: " custom-version ", ProfileVersion: " custom-profile ", LeaseUntil: now.Add(15 * time.Minute),
		ConfigurationHash: " config ", SourceFingerprint: " source "}
	got := NormalizeCommunityRunClaimInput(supplied, now)
	require.Equal(t, supplied.LeaseUntil, got.LeaseUntil)
	require.Equal(t, "explicit", got.WindowKey)
	require.Equal(t, "custom", got.AlgorithmKind)
	require.Equal(t, "custom-version", got.AlgorithmVersion)
	require.Equal(t, "custom-profile", got.ProfileVersion)
	require.Equal(t, "config", got.ConfigurationHash)
	require.Equal(t, "source", got.SourceFingerprint)
	require.NoError(t, ValidateCommunityRunClaimInput(got))
}

func TestCommunityRecallNativeBoundsAndUUIDSemantics(t *testing.T) {
	teamID, _, _, _, relationshipID, _, _ := communityPolicyTestIDs()
	for _, tc := range []struct{ value, communities, relationships int }{
		{-1, 3, 5}, {0, 3, 5}, {1, 1, 1}, {10, 10, 10}, {100, 10, 20},
	} {
		input := NormalizeCommunityRecallInput(CommunityRecallInput{TeamID: " " + teamID + " ", Query: " query ", Limit: tc.value, RelationshipLimit: tc.value,
			ReturnedEvidenceIDs: []string{"bad", " " + relationshipID + " ", relationshipID},
			CoveredGroupKeys:    []string{"z", " a "}, ExcludedGroupKeys: []string{"b", "z"}})
		require.Equal(t, tc.communities, input.Limit)
		require.Equal(t, tc.relationships, input.RelationshipLimit)
		require.Equal(t, []string{relationshipID}, input.ReturnedEvidenceIDs)
		require.Equal(t, []string{"a", "b", "z"}, input.CoveredGroupKeys)
		require.Equal(t, "query", input.Query)
		require.NoError(t, ValidateCommunityRecallInput(input))
	}
	invalid := NormalizeCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, KnownRelationshipIDs: []string{" bad ", "bad"}})
	require.Equal(t, []string{"bad"}, invalid.KnownRelationshipIDs)
	require.ErrorContains(t, ValidateCommunityDiscoveryInput(invalid), "known_relationship_ids contains invalid UUID")
	require.Equal(t, []string{relationshipID, relationshipID}, NormalizeCommunitySummaryUUIDs([]string{relationshipID, "bad", relationshipID}))
	upper := strings.ToUpper("abcdefab-cdef-4abc-8def-abcdefabcdef")
	require.Equal(t, []string{upper}, NormalizeCommunityIDs([]string{upper}))
	order := RecallCommunityMatchOrder()
	order[0] = RecallCommunityRemainingMatch
	require.Equal(t, RecallCommunityReturnedEvidenceOverlap, RecallCommunityMatchOrder()[0])
}

func TestCommunityPublicationPreservesSuppliedLogicalIdentityAndVersions(t *testing.T) {
	teamID, _, runID, communityID, relationshipID, profileID, entityID := communityPolicyTestIDs()
	logicalID := "88888888-8888-4888-8888-888888888888"
	input := NormalizeCommunitySnapshotPublishInput(CommunitySnapshotPublishInput{
		TeamID: teamID, RunID: runID, AlgorithmKind: " custom-kind ", AlgorithmVersion: " custom-version ", ProfileVersion: " custom-profile ",
		ConfigurationHash: " custom-config ", SourceFingerprint: " custom-source ",
		Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: " " + logicalID + " ", Summary: " summary ", SummaryVersion: " custom-summary ",
			Memberships: []CommunityMembershipInput{{EntityID: entityID, MembershipScore: 1}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 2}},
		}},
	})
	require.NoError(t, ValidateCommunitySnapshotPublishInput(input))
	require.Equal(t, communityID, input.Communities[0].CommunityID)
	require.Equal(t, logicalID, input.Communities[0].LogicalCommunityID)
	require.Equal(t, "custom-summary", input.Communities[0].SummaryVersion)
	require.Equal(t, "custom-kind", input.AlgorithmKind)
	require.Equal(t, "custom-version", input.AlgorithmVersion)
	require.Equal(t, "custom-profile", input.ProfileVersion)
	require.Equal(t, "custom-config", input.ConfigurationHash)
	require.Equal(t, "custom-source", input.SourceFingerprint)
}
