package postgres

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func communityTestIDs() (teamID, spaceID, runID, communityID, relationshipID, profileID, entityID string) {
	return "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666",
		"77777777-7777-7777-7777-777777777777"
}

func expectCommunityFenceWithArgs(mock sqlmock.Sqlmock, teamID, spaceID string, generation int64) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, generation")).
		WithArgs(teamID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation"}).AddRow(spaceID, generation))
}

func runRows(teamID, runID string, completedAt any, claimed bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"team_id", "run_id", "window_key", "status", "algorithm_kind", "algorithm_version", "profile_version",
		"configuration_hash", "source_fingerprint", "node_count", "edge_count", "community_count", "max_nodes",
		"max_edges", "error", "started_at", "completed_at", "claimed",
	}).AddRow(teamID, runID, "2026-09-08", "running", CommunityAlgorithmKind, CommunityAlgorithmVersion,
		CommunityProfileVersion, "config", "source", 3, 4, 1, 100, 200, "", time.Now().UTC(), completedAt, claimed)
}

func communityRecordRows(teamID, communityID, runID string, supersededAt any) *sqlmock.Rows {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	return sqlmock.NewRows([]string{
		"team_id", "community_id", "logical_community_id", "run_id", "ordinal", "status", "summary", "summary_version",
		"member_count", "source_count", "top_entities", "top_predicates", "source_fingerprint", "stale_reason",
		"created_at", "updated_at", "superseded_at",
	}).AddRow(teamID, communityID, communityID, runID, 1, "current", "summary", "v1", 2, 1,
		pq.StringArray{"Entity"}, pq.StringArray{"uses"}, "source", "", now, now.Add(time.Minute), supersededAt)
}

func TestCommunityAdapterPurePolicies(t *testing.T) {
	teamID, _, runID, communityID, relationshipID, profileID, entityID := communityTestIDs()
	long := strings.Repeat("x", 600)

	assert.Equal(t, "fallback", normalizeCommunityLogicalID(CommunityPublishRecord{LogicalCommunityID: " ", CommunityID: " fallback "}))
	assert.Equal(t, "logical", normalizeCommunityLogicalID(CommunityPublishRecord{LogicalCommunityID: " logical ", CommunityID: communityID}))
	assert.Equal(t, "", truncateCommunityError("   "))
	assert.Len(t, truncateCommunityError(long), 512)
	assert.Equal(t, "value", truncateCommunityError(" value "))
	encoded, err := marshalCommunitySnapshot(nil)
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(encoded))
	_, err = marshalCommunitySnapshot([]map[string]any{{"bad": func() {}}})
	assert.Error(t, err)

	claim := normalizeCommunityRunClaimInput(CommunityRunClaimInput{TeamID: " " + teamID + " ", WindowKey: " window ", AlgorithmKind: " kind ", AlgorithmVersion: " version ", ProfileVersion: " profile ", ConfigurationHash: " config ", SourceFingerprint: " source "})
	assert.Equal(t, "window", claim.WindowKey)
	assert.Equal(t, "kind", claim.AlgorithmKind)
	assert.False(t, claim.LeaseUntil.IsZero())
	complete := normalizeCommunityRunCompleteInput(CommunityRunCompleteInput{TeamID: " " + teamID + " ", RunID: " " + runID + " ", Error: " error "})
	assert.Equal(t, "completed", complete.Status)

	assert.NoError(t, validateCommunityRunClaimInput(CommunityRunClaimInput{TeamID: teamID, WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v"}))
	for _, input := range []CommunityRunClaimInput{
		{WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v"},
		{TeamID: teamID, AlgorithmKind: "a", AlgorithmVersion: "v"},
		{TeamID: teamID, WindowKey: "w"},
		{TeamID: teamID, WindowKey: "w", AlgorithmKind: "a", AlgorithmVersion: "v", MaxNodes: -1},
	} {
		assert.Error(t, validateCommunityRunClaimInput(input))
	}
	assert.NoError(t, validateCommunityRunCompleteInput(CommunityRunCompleteInput{TeamID: teamID, RunID: runID, Status: "completed"}))
	for _, input := range []CommunityRunCompleteInput{
		{RunID: runID, Status: "completed"},
		{TeamID: teamID, Status: "completed"},
		{TeamID: teamID, RunID: runID, Status: "unknown"},
		{TeamID: teamID, RunID: runID, Status: "completed", NodeCount: -1},
	} {
		assert.Error(t, validateCommunityRunCompleteInput(input))
	}

	assert.Equal(t, 500, normalizeCommunityInputListInput(CommunityInputListInput{TeamID: teamID}).Limit)
	assert.Equal(t, 5001, normalizeCommunityInputListInput(CommunityInputListInput{TeamID: teamID, Limit: 9000}).Limit)
	assert.NoError(t, validateCommunityInputListInput(CommunityInputListInput{TeamID: teamID}))
	assert.Error(t, validateCommunityInputListInput(CommunityInputListInput{}))

	publish := normalizeCommunitySnapshotPublishInput(CommunitySnapshotPublishInput{TeamID: " " + teamID + " ", RunID: " " + runID + " ", Communities: []CommunityPublishRecord{{CommunityID: " " + communityID + " ", Memberships: []CommunityMembershipInput{{EntityID: " " + entityID + " "}}, Sources: []CommunitySourceInput{{RelationshipID: " " + relationshipID + " ", OwnerProfileID: " " + profileID + " "}}}}})
	assert.Equal(t, communityID, publish.Communities[0].LogicalCommunityID)
	assert.Equal(t, entityID, publish.Communities[0].Memberships[0].EntityID)
	assert.Equal(t, relationshipID, publish.Communities[0].Sources[0].RelationshipID)
	assert.Equal(t, 500, normalizeCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID}).Limit)
	assert.Equal(t, 5000, normalizeCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID, Limit: 9000}).Limit)
	assert.NoError(t, validateCommunityStalenessInput(CommunityStalenessInput{TeamID: teamID}))
	assert.Error(t, validateCommunityStalenessInput(CommunityStalenessInput{}))

	validPublish := CommunitySnapshotPublishInput{TeamID: teamID, RunID: runID, SourceFingerprint: "source", Communities: []CommunityPublishRecord{{CommunityID: communityID, LogicalCommunityID: communityID, Summary: "summary", Memberships: []CommunityMembershipInput{{EntityID: entityID, MembershipScore: 0}}, Sources: []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1}}}}}
	assert.NoError(t, validateCommunitySnapshotPublishInput(validPublish))
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
		assert.Error(t, validateCommunitySnapshotPublishInput(input))
	}

	assert.Equal(t, 20, normalizeCommunityListInput(CommunityListInput{TeamID: teamID}).Limit)
	assert.Equal(t, 100, normalizeCommunityListInput(CommunityListInput{TeamID: teamID, Limit: 900}).Limit)
	assert.NoError(t, validateCommunityListInput(CommunityListInput{TeamID: teamID, Status: "current"}))
	assert.Error(t, validateCommunityListInput(CommunityListInput{TeamID: teamID, Status: "invalid"}))
	assert.NoError(t, validateCommunityGetInput(CommunityGetInput{TeamID: teamID, CommunityID: communityID}))
	assert.Error(t, validateCommunityGetInput(CommunityGetInput{TeamID: teamID}))
	assert.Error(t, validateCommunityGetInput(CommunityGetInput{CommunityID: communityID}))

	assert.Equal(t, 5, normalizeCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID}).Limit)
	assert.Equal(t, 20, normalizeCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, Limit: 90}).Limit)
	assert.NoError(t, validateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, KnownRelationshipIDs: []string{relationshipID}, ExpandFromEntityIDs: []string{entityID}}))
	assert.Error(t, validateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, KnownRelationshipIDs: []string{"bad"}}))
	assert.Error(t, validateCommunityDiscoveryInput(CommunityDiscoveryInput{TeamID: teamID, ExpandFromEntityIDs: []string{"bad"}}))
	assert.Equal(t, []string{relationshipID}, normalizeRecallUUIDList([]string{" ", relationshipID, relationshipID}))
	assert.True(t, communityStatusValid("current"))
	assert.False(t, communityStatusValid("other"))

	recall := normalizeCommunityRecallInput(CommunityRecallInput{TeamID: " " + teamID + " ", ExcludedGroupKeys: []string{" z ", "z", "a"}})
	assert.Equal(t, []string{"a", "z"}, recall.CoveredGroupKeys)
	assert.Equal(t, 3, recall.Limit)
	assert.Equal(t, 5, recall.RelationshipLimit)
	assert.Equal(t, 10, normalizeCommunityRecallInput(CommunityRecallInput{TeamID: teamID, Limit: 99}).Limit)
	assert.Equal(t, 20, normalizeCommunityRecallInput(CommunityRecallInput{TeamID: teamID, RelationshipLimit: 99}).RelationshipLimit)
	assert.NoError(t, validateCommunityRecallInput(CommunityRecallInput{TeamID: teamID}))
	assert.Error(t, validateCommunityRecallInput(CommunityRecallInput{}))
	assert.Equal(t, []string{relationshipID}, normalizeCommunityIDs([]string{" ", relationshipID, "bad", relationshipID}))
	assert.Equal(t, []string{"a", "b"}, normalizeCommunityStrings([]string{" a ", "a", "b", ""}))
	assert.Equal(t, []string{"a", "b"}, appendUniqueCommunityStrings([]string{"b"}, "a", "b", ""))

	assert.Equal(t, []CommunitySourceInput{{RelationshipID: relationshipID, RelationshipVersion: 1}}, flattenCommunitySources([]CommunityPublishRecord{{Sources: []CommunitySourceInput{{RelationshipID: relationshipID, RelationshipVersion: 1}, {RelationshipID: relationshipID, RelationshipVersion: 1}}}}))
}

func TestCommunityAdapterScanHelpers(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID, _, runID, communityID, _, _, _ := communityTestIDs()
	started := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	mock.ExpectQuery("scan run").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "run_id", "window_key", "status", "algorithm_kind", "algorithm_version", "profile_version", "configuration_hash", "source_fingerprint", "node_count", "edge_count", "community_count", "max_nodes", "max_edges", "error", "started_at", "completed_at", "claimed",
	}).AddRow(teamID, runID, "window", "completed", "kind", "version", "profile", "config", "source", 1, 2, 3, 4, 5, "", started, completed, true))
	rows, err := db.Raw("scan run").Rows()
	require.NoError(t, err)
	require.True(t, rows.Next())
	run, err := scanCommunityRun(rows)
	require.NoError(t, err)
	assert.Equal(t, &completed, run.CompletedAt)
	assert.True(t, run.Claimed)
	require.NoError(t, rows.Close())

	mock.ExpectQuery("scan records").WillReturnRows(communityRecordRows(teamID, communityID, runID, completed))
	rows, err = db.Raw("scan records").Rows()
	require.NoError(t, err)
	records, err := scanCommunityRecords(rows)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, &completed, records[0].SupersededAt)
	require.NoError(t, rows.Close())

	mock.ExpectQuery("scan empty").WillReturnRows(sqlmock.NewRows([]string{"value"}))
	rows, err = db.Raw("scan empty").Rows()
	require.NoError(t, err)
	records, err = scanCommunityRecords(rows)
	require.NoError(t, err)
	assert.Empty(t, records)
	require.NoError(t, rows.Close())
	assert.NoError(t, mock.ExpectationsWereMet())
}
