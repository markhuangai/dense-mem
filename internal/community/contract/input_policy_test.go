package contract

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func communityPolicyTestIDs() (teamID, spaceID, runID, communityID, relationshipID, profileID, entityID string) {
	return "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666",
		"77777777-7777-7777-7777-777777777777"
}

func TestCommunityContractInputPolicies(t *testing.T) {
	teamID, _, runID, communityID, relationshipID, profileID, entityID := communityPolicyTestIDs()
	long := strings.Repeat("x", 600)

	assert.Equal(t, "", TruncateCommunityDiagnostic("   "))
	assert.Len(t, TruncateCommunityDiagnostic(long), 512)
	unicodeValue := strings.Repeat("é", 513)
	truncatedUnicode := TruncateCommunityDiagnostic(unicodeValue)
	assert.Equal(t, 512, utf8.RuneCountInString(truncatedUnicode))
	assert.True(t, utf8.ValidString(truncatedUnicode))
	assert.Equal(t, "value", TruncateCommunityDiagnostic(" value "))

	claim := NormalizeCommunityRunClaimInput(CommunityRunClaimInput{TeamID: " " + teamID + " ", WindowKey: " window ", AlgorithmKind: " kind ", AlgorithmVersion: " version ", ProfileVersion: " profile ", ConfigurationHash: " config ", SourceFingerprint: " source "}, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	assert.Equal(t, "window", claim.WindowKey)
	assert.Equal(t, "kind", claim.AlgorithmKind)
	assert.False(t, claim.LeaseUntil.IsZero())
	complete := NormalizeCommunityRunCompleteInput(CommunityRunCompleteInput{TeamID: " " + teamID + " ", RunID: " " + runID + " ", Error: " error "})
	assert.Equal(t, "completed", complete.Status)

	assert.NoError(t, ValidateCommunityRunClaimInput(CommunityRunClaimInput{TeamID: teamID, WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v"}))
	for _, input := range []CommunityRunClaimInput{
		{WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v"},
		{TeamID: teamID, AlgorithmKind: "a", AlgorithmVersion: "v"},
		{TeamID: teamID, WindowKey: "w"},
		{TeamID: teamID, WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v", MaxNodes: -1},
	} {
		assert.Error(t, ValidateCommunityRunClaimInput(input))
	}
	assert.NoError(t, ValidateCommunityRunCompleteInput(CommunityRunCompleteInput{TeamID: teamID, RunID: runID, Status: "completed"}))
	for _, input := range []CommunityRunCompleteInput{
		{RunID: runID, Status: "completed"},
		{TeamID: teamID, Status: "completed"},
		{TeamID: teamID, RunID: runID, Status: "unknown"},
		{TeamID: teamID, RunID: runID, Status: "completed", NodeCount: -1},
	} {
		assert.Error(t, ValidateCommunityRunCompleteInput(input))
	}

	assert.Equal(t, 500, NormalizeCommunityInputListInput(CommunityInputListInput{TeamID: teamID}).Limit)
	assert.Equal(t, 5001, NormalizeCommunityInputListInput(CommunityInputListInput{TeamID: teamID, Limit: 9000}).Limit)
	assert.NoError(t, ValidateCommunityInputListInput(CommunityInputListInput{TeamID: teamID}))
	assert.Error(t, ValidateCommunityInputListInput(CommunityInputListInput{}))

	publish := NormalizeCommunitySnapshotPublishInput(CommunitySnapshotPublishInput{TeamID: " " + teamID + " ", RunID: " " + runID + " ", Communities: []CommunityPublishRecord{{CommunityID: " " + communityID + " ", Memberships: []CommunityMembershipInput{{EntityID: " " + entityID + " "}}, Sources: []CommunitySourceInput{{RelationshipID: " " + relationshipID + " ", OwnerProfileID: " " + profileID + " "}}}}})
	assert.Equal(t, communityID, publish.Communities[0].LogicalCommunityID)
	assert.Equal(t, entityID, publish.Communities[0].Memberships[0].EntityID)
	assert.Equal(t, relationshipID, publish.Communities[0].Sources[0].RelationshipID)
	assert.Equal(t, 500, NormalizeCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID}).Limit)
	assert.Equal(t, 5000, NormalizeCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID, Limit: 9000}).Limit)
	assert.NoError(t, ValidateCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID}))
	assert.Error(t, ValidateCommunityStalenessInput(CommunityStalenessInput{}))

	validPublish := CommunitySnapshotPublishInput{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "summary", Memberships: []CommunityMembershipInput{{EntityID: entityID, MembershipScore: 0}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}}
	assert.NoError(t, ValidateCommunitySnapshotPublishInput(validPublish))
	for _, input := range []CommunitySnapshotPublishInput{
		{RunID: runID, SourceFingerprint: "source"},
		{TeamID: teamID, SourceFingerprint: "source"},
		{TeamID: teamID, RunID: runID},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", NodeCount: -1},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: "bad", LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: "bad", Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: "bad"}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID, MembershipScore: 2}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: "bad", OwnerProfileID: profileID, RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: "bad", RelationshipVersion: 1}}}}},
		{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "s", Memberships: []CommunityMembershipInput{{EntityID: entityID}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID}}}}},
	} {
		assert.Error(t, ValidateCommunitySnapshotPublishInput(input))
	}

	assert.Equal(t, 20, NormalizeCommunityListInput(CommunityListInput{TeamID: teamID}).Limit)
	assert.Equal(t, 100, NormalizeCommunityListInput(CommunityListInput{TeamID: teamID, Limit: 900}).Limit)
	assert.NoError(t, ValidateCommunityListInput(CommunityListInput{TeamID: teamID, Status: "current"}))
	assert.Error(t, ValidateCommunityListInput(CommunityListInput{TeamID: teamID, Status: "invalid"}))
	assert.NoError(t, ValidateCommunityGetInput(CommunityGetInput{TeamID: teamID, CommunityID: communityID}))
	assert.Error(t, ValidateCommunityGetInput(CommunityGetInput{TeamID: teamID}))
	assert.Error(t, ValidateCommunityGetInput(CommunityGetInput{CommunityID: communityID}))

	assert.Equal(t, 5, NormalizeCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID}).Limit)
	assert.Equal(t, 20, NormalizeCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, Limit: 90}).Limit)
	assert.NoError(t, ValidateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, KnownRelationshipIDs: []string{relationshipID}, ExpandFromEntityIDs: []string{entityID}}))
	assert.Error(t, ValidateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, KnownRelationshipIDs: []string{"bad"}}))
	assert.Error(t, ValidateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, ExpandFromEntityIDs: []string{"bad"}}))
	assert.Equal(t, []string{relationshipID}, normalizeRecallUUIDList([]string{" ", relationshipID, relationshipID}))
	assert.True(t, communityStatusValid("current"))
	assert.False(t, communityStatusValid("other"))

	recall := NormalizeCommunityRecallInput(CommunityRecallInput{TeamID: " " + teamID + " ", ExcludedGroupKeys: []string{" z ", "z", "a"}})
	assert.Equal(t, []string{"a", "z"}, recall.CoveredGroupKeys)
	assert.Equal(t, 3, recall.Limit)
	assert.Equal(t, 5, recall.RelationshipLimit)
	assert.Equal(t, 10, NormalizeCommunityRecallInput(CommunityRecallInput{TeamID: teamID, Limit: 99}).Limit)
	assert.Equal(t, 20, NormalizeCommunityRecallInput(CommunityRecallInput{TeamID: teamID, RelationshipLimit: 99}).RelationshipLimit)
	assert.NoError(t, ValidateCommunityRecallInput(CommunityRecallInput{TeamID: teamID}))
	assert.Error(t, ValidateCommunityRecallInput(CommunityRecallInput{}))
	assert.Equal(t, []string{relationshipID}, NormalizeCommunityIDs([]string{" ", relationshipID, "bad", relationshipID}))
	assert.Equal(t, []string{"a", "b"}, normalizeCommunityStrings([]string{" a ", "a", "b", ""}))
	assert.Equal(t, []string{"a", "b"}, appendUniqueCommunityStrings([]string{"b"}, "a", "b", ""))
	assert.Equal(t, []string{relationshipID}, NormalizeCommunitySummaryUUIDs([]string{" " + relationshipID + " ", "not-a-uuid"}))

}
