package postgres

import (
	"regexp"
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

func TestCommunityAdapterEncodingAndSources(t *testing.T) {
	_, _, _, _, relationshipID, _, _ := communityTestIDs()
	encoded, err := marshalCommunitySnapshot(nil)
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(encoded))
	_, err = marshalCommunitySnapshot([]map[string]any{{"bad": func() {}}})
	assert.Error(t, err)
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
