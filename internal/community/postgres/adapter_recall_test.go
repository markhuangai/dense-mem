package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
