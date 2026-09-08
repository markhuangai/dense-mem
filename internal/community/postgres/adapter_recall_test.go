package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommunityStoreRecallCommunitiesHydratesAndTruncates(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID, _, _, communityID, relationshipID, _, entityID := communityTestIDs()
	otherCommunityID := "88888888-8888-8888-8888-888888888888"
	createdAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{
		"community_id", "logical_community_id", "rank", "summary", "member_count", "source_count", "top_predicates",
	}).AddRow(communityID, communityID, 1, "summary", 3, 2, pq.StringArray{"uses"}).AddRow(otherCommunityID, otherCommunityID, 2, "empty", 1, 1, pq.StringArray{"works_on"}))
	mock.ExpectQuery(regexp.QuoteMeta("WITH ranked_memberships AS")).WillReturnRows(sqlmock.NewRows([]string{
		"community_id", "entity_id", "entity_name",
	}).AddRow(communityID, entityID, "Entity").AddRow(otherCommunityID, entityID, "Other"))
	mock.ExpectQuery(regexp.QuoteMeta("WITH latest_support AS")).WillReturnRows(sqlmock.NewRows([]string{
		"community_id", "relationship_id", "semantic_group_key", "subject_entity_id", "subject_name", "predicate_key", "object_entity_id", "object_value_id", "object_name", "object_value_type", "object_value", "polarity", "evidence_ids", "source_rank", "created_at",
	}).AddRow(communityID, relationshipID, "group", entityID, "Subject", "uses", entityID, "", "Object", "", "", "positive", pq.StringArray{"e1"}, 1, createdAt).
		AddRow(communityID, "99999999-9999-9999-9999-999999999999", "group2", entityID, "Subject", "works_on", entityID, "", "Object", "", "", "positive", pq.StringArray{"e2"}, 2, createdAt.Add(time.Minute)))

	store := NewStore(db, communityPassthroughRLS{})
	communities, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "summary", RelationshipLimit: 1})
	require.NoError(t, err)
	require.Len(t, communities, 1)
	assert.Equal(t, communityID, communities[0].CommunityID)
	assert.Equal(t, []string{"Entity"}, []string{communities[0].TopEntities[0].Name})
	assert.Len(t, communities[0].Relationships, 1)
	assert.True(t, communities[0].RelationshipsTruncated)
	assert.Equal(t, teamID, communities[0].Relationships[0].TeamID)
	assert.Equal(t, []string{"e1"}, communities[0].Relationships[0].EvidenceIDs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommunityStoreRecallCommunitiesSkipsEmptyAndHandlesErrors(t *testing.T) {
	teamID, _, _, communityID, _, _, _ := communityTestIDs()
	t.Run("empty query and no expansion", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{"community_id"}))
		store := NewStore(db, communityPassthroughRLS{})
		got, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("no matches", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{"community_id"}))
		store := NewStore(db, communityPassthroughRLS{})
		got, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("main query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		queryErr := errors.New("recall query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("top entity query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "logical_community_id", "rank", "summary", "member_count", "source_count", "top_predicates",
		}).AddRow(communityID, communityID, 1, "summary", 1, 1, pq.StringArray{"uses"}))
		queryErr := errors.New("top entity query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH ranked_memberships AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("relationship query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "logical_community_id", "rank", "summary", "member_count", "source_count", "top_predicates",
		}).AddRow(communityID, communityID, 1, "summary", 1, 1, pq.StringArray{"uses"}))
		mock.ExpectQuery(regexp.QuoteMeta("WITH ranked_memberships AS")).WillReturnRows(sqlmock.NewRows([]string{"community_id", "entity_id", "entity_name"}))
		queryErr := errors.New("relationship query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH latest_support AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("row scan errors", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "logical_community_id", "rank", "summary", "member_count", "source_count", "top_predicates",
		}).AddRow("bad", "bad", "bad", "summary", 1, 1, pq.StringArray{"uses"}))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())

		db, mock = newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH params AS")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "logical_community_id", "rank", "summary", "member_count", "source_count", "top_predicates",
		}).AddRow(communityID, communityID, 1, "summary", 1, 1, pq.StringArray{"uses"}))
		mock.ExpectQuery(regexp.QuoteMeta("WITH ranked_memberships AS")).WillReturnRows(sqlmock.NewRows([]string{"community_id", "entity_id", "entity_name"}).AddRow("bad", 1, "name"))
		store = NewStore(db, communityPassthroughRLS{})
		_, err = store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: teamID, Query: "query"})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreListSemanticGroups(t *testing.T) {
	teamID, _, _, _, relationshipID, _, _ := communityTestIDs()
	t.Run("empty IDs", func(t *testing.T) {
		store := &Store{}
		groups, err := store.ListCommunitySemanticGroups(context.Background(), CommunityCoverageInput{TeamID: teamID})
		require.NoError(t, err)
		assert.Empty(t, groups)
	})
	t.Run("success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH latest_support AS")).WillReturnRows(sqlmock.NewRows([]string{"semantic_group_key"}).AddRow("group-a").AddRow("group-b"))
		store := NewStore(db, communityPassthroughRLS{})
		groups, err := store.ListCommunitySemanticGroups(context.Background(), CommunityCoverageInput{TeamID: teamID, RelationshipIDs: []string{relationshipID}})
		require.NoError(t, err)
		assert.Equal(t, []string{"group-a", "group-b"}, groups)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("query and scan errors", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		queryErr := errors.New("coverage query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH latest_support AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ListCommunitySemanticGroups(context.Background(), CommunityCoverageInput{TeamID: teamID, RelationshipIDs: []string{relationshipID}})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())

		db, mock = newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH latest_support AS")).WillReturnRows(sqlmock.NewRows([]string{"semantic_group_key"}).AddRow(nil))
		store = NewStore(db, communityPassthroughRLS{})
		_, err = store.ListCommunitySemanticGroups(context.Background(), CommunityCoverageInput{TeamID: teamID, RelationshipIDs: []string{relationshipID}})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreRecallDiscovery(t *testing.T) {
	teamID, _, _, communityID, relationshipID, _, entityID := communityTestIDs()
	t.Run("empty input", func(t *testing.T) {
		store := &Store{}
		paths, err := store.RecallCommunityDiscovery(context.Background(), CommunityDiscoveryInput{TeamID: teamID})
		require.NoError(t, err)
		assert.Empty(t, paths)
	})
	t.Run("success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH matched_communities AS")).WillReturnRows(sqlmock.NewRows([]string{
			"community_id", "community_rank", "source_rank", "relationship_id", "subject_entity_id", "subject_name", "predicate_key", "object_entity_id", "object_name", "polarity", "evidence_ids",
		}).AddRow(communityID, 1, 1, relationshipID, entityID, "Subject", "uses", entityID, "Object", "positive", pq.StringArray{"e1"}))
		store := NewStore(db, communityPassthroughRLS{})
		paths, err := store.RecallCommunityDiscovery(context.Background(), CommunityDiscoveryInput{TeamID: teamID, Query: "query"})
		require.NoError(t, err)
		require.Len(t, paths, 1)
		assert.Equal(t, relationshipID, paths[0].Relationship.RelationshipID)
		assert.Equal(t, []string{"e1"}, paths[0].EvidenceIDs)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("query and scan errors", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		queryErr := errors.New("discovery query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH matched_communities AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RecallCommunityDiscovery(context.Background(), CommunityDiscoveryInput{TeamID: teamID, Query: "query"})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())

		db, mock = newCommunitySQLMockDB(t)
		mock.ExpectQuery(regexp.QuoteMeta("WITH matched_communities AS")).WillReturnRows(sqlmock.NewRows([]string{"community_id"}).AddRow("bad"))
		store = NewStore(db, communityPassthroughRLS{})
		_, err = store.RecallCommunityDiscovery(context.Background(), CommunityDiscoveryInput{TeamID: teamID, Query: "query"})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreRecordSummaryAttempt(t *testing.T) {
	teamID, spaceID, runID, communityID, relationshipID, _, _ := communityTestIDs()
	db, mock := newCommunitySQLMockDB(t)
	expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_summary_attempts")).WillReturnResult(sqlmock.NewResult(0, 1))
	store := NewStore(db, communityPassthroughRLS{})
	err := store.RecordCommunitySummaryAttempt(context.Background(), CommunitySummaryAttemptInput{
		TeamID: teamID, RunID: runID, CommunityID: communityID, Attempt: 1, AdmittedRelationshipIDs: []string{relationshipID},
		AdmittedSupportQuotes: []domain.CommunitySummarySupportQuote{{EvidenceID: "e1", Quote: "quote"}}, ResponseSummary: "summary", Valid: true,
	})
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())

	t.Run("validation", func(t *testing.T) {
		store := &Store{}
		for _, input := range []CommunitySummaryAttemptInput{
			{RunID: runID, Attempt: 1}, {TeamID: teamID, Attempt: 1}, {TeamID: teamID, RunID: runID, Attempt: 0},
			{TeamID: teamID, RunID: runID, CommunityID: "bad", Attempt: 1},
		} {
			assert.Error(t, store.RecordCommunitySummaryAttempt(context.Background(), input))
		}
	})
	t.Run("query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		queryErr := errors.New("summary attempt failed")
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_summary_attempts")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.RecordCommunitySummaryAttempt(context.Background(), CommunitySummaryAttemptInput{TeamID: teamID, RunID: runID, Attempt: 1})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
