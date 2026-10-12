package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommunityStoreListInputs(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID, spaceID, _, _, relationshipID, profileID, entityID := communityTestIDs()
	expectCommunityFenceWithArgs(mock, teamID, spaceID, 5)
	mock.ExpectQuery(regexp.QuoteMeta("WITH canonical_names AS")).WillReturnRows(sqlmock.NewRows([]string{
		"relationship_id", "owner_profile_id", "version", "subject_entity_id", "subject_name", "predicate_key", "predicate_version",
		"object_entity_id", "object_name", "object_value_id", "object_value_type", "object_value", "semantic_group_key", "evidence_ids", "evidence_quotes",
	}).AddRow(relationshipID, profileID, 2, entityID, "Subject", "uses", 1, "", "Object", "", "", "", "group", pq.StringArray{"e1"}, `[ {"evidence_id":"e1","quote":"quote"} ]`))
	store := NewStore(db, communityPassthroughRLS{})
	items, err := store.ListCommunityInputs(context.Background(), CommunityInputListInput{TeamID: teamID, Limit: 3})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, relationshipID, items[0].RelationshipID)
	assert.Equal(t, "quote", items[0].EvidenceQuotes[0].Quote)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommunityStoreListInputsErrors(t *testing.T) {
	teamID, spaceID, _, _, _, _, _ := communityTestIDs()
	t.Run("query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 5)
		queryErr := errors.New("inputs query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH canonical_names AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ListCommunityInputs(context.Background(), CommunityInputListInput{TeamID: teamID})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("scan error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 5)
		mock.ExpectQuery(regexp.QuoteMeta("WITH canonical_names AS")).WillReturnRows(sqlmock.NewRows([]string{"relationship_id"}).AddRow("bad"))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ListCommunityInputs(context.Background(), CommunityInputListInput{TeamID: teamID})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("invalid evidence JSON", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 5)
		mock.ExpectQuery(regexp.QuoteMeta("WITH canonical_names AS")).WillReturnRows(sqlmock.NewRows([]string{
			"relationship_id", "owner_profile_id", "version", "subject_entity_id", "subject_name", "predicate_key", "predicate_version",
			"object_entity_id", "object_name", "object_value_id", "object_value_type", "object_value", "semantic_group_key", "evidence_ids", "evidence_quotes",
		}).AddRow("rel", "profile", 1, "entity", "subject", "uses", 1, "", "object", "", "", "", "group", pq.StringArray{"e1"}, `{bad}`))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ListCommunityInputs(context.Background(), CommunityInputListInput{TeamID: teamID})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreReadMethods(t *testing.T) {
	teamID, spaceID, runID, communityID, _, _, _ := communityTestIDs()
	t.Run("count", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)::int")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
		store := NewStore(db, communityPassthroughRLS{})
		count, err := store.CountCurrentCommunities(context.Background(), teamID)
		require.NoError(t, err)
		assert.Equal(t, 4, count)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("get success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, community_id::text")).WillReturnRows(communityRecordRows(teamID, communityID, runID, nil))
		store := NewStore(db, communityPassthroughRLS{})
		record, err := store.GetCommunity(context.Background(), CommunityGetInput{TeamID: teamID, CommunityID: communityID})
		require.NoError(t, err)
		assert.Equal(t, communityID, record.CommunityID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("get not found", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, community_id::text")).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.GetCommunity(context.Background(), CommunityGetInput{TeamID: teamID, CommunityID: communityID})
		assert.ErrorIs(t, err, ErrCommunityNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("latest success and empty", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, run_id::text")).WillReturnRows(runRows(teamID, runID, time.Now().UTC(), false))
		store := NewStore(db, communityPassthroughRLS{})
		run, err := store.LatestCommunityRun(context.Background(), teamID)
		require.NoError(t, err)
		require.NotNil(t, run)
		assert.False(t, run.Claimed)
		assert.NoError(t, mock.ExpectationsWereMet())

		db, mock = newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, run_id::text")).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
		store = NewStore(db, communityPassthroughRLS{})
		run, err = store.LatestCommunityRun(context.Background(), teamID)
		require.NoError(t, err)
		assert.Nil(t, run)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("lineage", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT record.community_id::text")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "logical_community_id", "group_keys", "summary_input_hash", "summary", "summary_version", "summary_provider_model", "summary_prompt_hash", "summary_response_hash",
		}).AddRow(communityID, communityID, pq.StringArray{"group"}, "input", "summary", "v1", "model", "prompt", "response"))
		store := NewStore(db, communityPassthroughRLS{})
		lineage, err := store.ListCurrentCommunityLineage(context.Background(), teamID)
		require.NoError(t, err)
		require.Len(t, lineage, 1)
		assert.Equal(t, []string{"group"}, lineage[0].GroupKeys)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreReadMethodErrors(t *testing.T) {
	teamID, spaceID, _, communityID, _, _, _ := communityTestIDs()
	tests := []struct {
		name  string
		query string
		call  func(*Store) error
	}{
		{"count", "SELECT count(*)::int", func(store *Store) error {
			_, err := store.CountCurrentCommunities(context.Background(), teamID)
			return err
		}},
		{"get", "SELECT team_id::text, community_id::text", func(store *Store) error {
			_, err := store.GetCommunity(context.Background(), CommunityGetInput{TeamID: teamID, CommunityID: communityID})
			return err
		}},
		{"latest", "SELECT team_id::text, run_id::text", func(store *Store) error { _, err := store.LatestCommunityRun(context.Background(), teamID); return err }},
		{"lineage", "SELECT record.community_id::text", func(store *Store) error {
			_, err := store.ListCurrentCommunityLineage(context.Background(), teamID)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newCommunitySQLMockDB(t)
			expectCommunityFenceWithArgs(mock, teamID, spaceID, 8)
			queryErr := errors.New(tc.name + " failed")
			mock.ExpectQuery(regexp.QuoteMeta(tc.query)).WillReturnError(queryErr)
			store := NewStore(db, communityPassthroughRLS{})
			err := tc.call(store)
			assert.ErrorIs(t, err, queryErr)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
