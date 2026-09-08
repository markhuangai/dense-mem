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

func TestCommunityStoreClaimRunSuccessAndLeaseFencing(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID, spaceID, runID, _, _, _, _ := communityTestIDs()
	expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
	mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).
		WillReturnRows(runRows(teamID, runID, nil, true))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE community_records")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := NewStore(db, communityPassthroughRLS{})
	run, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{
		TeamID: teamID, WindowKey: "window", SourceFingerprint: "source", LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.True(t, run.Claimed)
	assert.Equal(t, runID, run.RunID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCommunityStoreClaimRunReturnsExistingOrErrors(t *testing.T) {
	teamID, spaceID, _, _, _, _, _ := communityTestIDs()
	t.Run("already claimed", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
		mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).
			WillReturnRows(runRows(teamID, "33333333-3333-3333-3333-333333333333", nil, false))
		store := NewStore(db, communityPassthroughRLS{})
		run, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window"})
		require.NoError(t, err)
		require.NotNil(t, run)
		assert.False(t, run.Claimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("empty result", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
		mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window"})
		assert.ErrorIs(t, err, ErrCommunityRunAlreadyClaimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
		queryErr := errors.New("claim query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window"})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreClaimRunHandlesScanAndStaleUpdateErrors(t *testing.T) {
	teamID, spaceID, _, _, _, _, _ := communityTestIDs()
	t.Run("scan error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
		mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(teamID))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window"})
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("stale update error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 7)
		mock.ExpectQuery(regexp.QuoteMeta("WITH attempted AS")).WillReturnRows(runRows(teamID, "33333333-3333-3333-3333-333333333333", nil, true))
		updateErr := errors.New("stale update failed")
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_records")).WillReturnError(updateErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window", SourceFingerprint: "source"})
		assert.ErrorIs(t, err, updateErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("fence error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		fenceErr := errors.New("fence failed")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, generation")).WithArgs(teamID).WillReturnError(fenceErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: teamID, WindowKey: "window"})
		assert.ErrorIs(t, err, fenceErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreCompleteAndRenewRun(t *testing.T) {
	teamID, spaceID, runID, _, _, _, _ := communityTestIDs()
	t.Run("complete success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 1))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.CompleteCommunityRun(context.Background(), CommunityRunCompleteInput{TeamID: teamID, RunID: runID, Status: "failed", Error: " " + string(make([]byte, 600))})
		require.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("complete no longer owned", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 0))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.CompleteCommunityRun(context.Background(), CommunityRunCompleteInput{TeamID: teamID, RunID: runID})
		assert.ErrorIs(t, err, ErrCommunityRunAlreadyClaimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("complete query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		queryErr := errors.New("complete failed")
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.CompleteCommunityRun(context.Background(), CommunityRunCompleteInput{TeamID: teamID, RunID: runID})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("renew success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 1))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.RenewCommunityRunLease(context.Background(), CommunityRunLeaseInput{TeamID: teamID, RunID: runID, LeaseUntil: time.Now().UTC().Add(time.Minute)})
		require.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("renew lost claim", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 0))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.RenewCommunityRunLease(context.Background(), CommunityRunLeaseInput{TeamID: teamID, RunID: runID, LeaseUntil: time.Now().UTC().Add(time.Minute)})
		assert.ErrorIs(t, err, ErrCommunityRunAlreadyClaimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("renew query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 4)
		queryErr := errors.New("renew failed")
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.RenewCommunityRunLease(context.Background(), CommunityRunLeaseInput{TeamID: teamID, RunID: runID, LeaseUntil: time.Now().UTC().Add(time.Minute)})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

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

func validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID string) CommunitySnapshotPublishInput {
	return CommunitySnapshotPublishInput{
		TeamID: teamID, RunID: runID, SourceFingerprint: "source", NodeCount: 2, EdgeCount: 1,
		SourceSnapshot: []map[string]any{{"relationship_id": relationshipID}},
		Communities: []CommunityPublishRecord{{
			CommunityID: communityID, LogicalCommunityID: communityID, Ordinal: 1, Summary: "summary", SummaryVersion: "v1",
			MemberCount: 1, SourceCount: 1, TopEntities: []string{"Entity"}, TopPredicates: []string{"uses"}, SourceFingerprint: "source",
			SummaryInputHash: "input", SummaryProviderModel: "model", SummaryPromptHash: "prompt", SummaryResponseHash: "response",
			Memberships: []CommunityMembershipInput{{EntityID: entityID, Rank: 1, MembershipScore: 1, SourceCount: 1}},
			Sources:     []CommunitySourceInput{{RelationshipID: relationshipID, OwnerProfileID: profileID, RelationshipVersion: 1, SourceRank: 1, SemanticGroupKey: "group", SourceStateHash: "state"}},
		}},
	}
}

func expectCommunitySourceCheck(mock sqlmock.Sqlmock, stale bool) {
	rows := sqlmock.NewRows([]string{"relationship_id"})
	if stale {
		rows.AddRow("55555555-5555-5555-5555-555555555555")
	}
	mock.ExpectQuery(regexp.QuoteMeta("WITH expected AS")).WillReturnRows(rows)
}

func TestCommunityStorePublishSnapshotSuccessAndErrors(t *testing.T) {
	teamID, spaceID, runID, communityID, relationshipID, profileID, entityID := communityTestIDs()
	t.Run("success", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		expectCommunitySourceCheck(mock, false)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_records")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_records")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_memberships")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_sources")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 1))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.PublishCommunitySnapshot(context.Background(), validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID))
		require.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("stale source", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		expectCommunitySourceCheck(mock, true)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.PublishCommunitySnapshot(context.Background(), validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID))
		assert.ErrorIs(t, err, ErrCommunitySourceStale)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("source query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		queryErr := errors.New("source check failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH expected AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.PublishCommunitySnapshot(context.Background(), validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID))
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("record insert error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		expectCommunitySourceCheck(mock, false)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_records")).WillReturnResult(sqlmock.NewResult(0, 1))
		insertErr := errors.New("record insert failed")
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_records")).WillReturnError(insertErr)
		store := NewStore(db, communityPassthroughRLS{})
		err := store.PublishCommunitySnapshot(context.Background(), validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID))
		assert.ErrorIs(t, err, insertErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("completion loses claim", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 2)
		expectCommunitySourceCheck(mock, false)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_records")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_records")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_memberships")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO community_sources")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE community_snapshot_runs")).WillReturnResult(sqlmock.NewResult(0, 0))
		store := NewStore(db, communityPassthroughRLS{})
		err := store.PublishCommunitySnapshot(context.Background(), validCommunityPublishInput(teamID, runID, communityID, relationshipID, profileID, entityID))
		assert.ErrorIs(t, err, ErrCommunityRunAlreadyClaimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCommunityStoreRefreshStaleness(t *testing.T) {
	db, mock := newCommunitySQLMockDB(t)
	teamID, spaceID, _, _, _, _, _ := communityTestIDs()
	expectCommunityFenceWithArgs(mock, teamID, spaceID, 3)
	mock.ExpectQuery(regexp.QuoteMeta("WITH stale AS")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	store := NewStore(db, communityPassthroughRLS{})
	updated, err := store.RefreshCommunityStaleness(context.Background(), CommunityStalenessInput{TeamID: teamID, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, updated)
	assert.NoError(t, mock.ExpectationsWereMet())

	t.Run("query error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 3)
		queryErr := errors.New("staleness query failed")
		mock.ExpectQuery(regexp.QuoteMeta("WITH stale AS")).WillReturnError(queryErr)
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RefreshCommunityStaleness(context.Background(), CommunityStalenessInput{TeamID: teamID})
		assert.ErrorIs(t, err, queryErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("scan error", func(t *testing.T) {
		db, mock := newCommunitySQLMockDB(t)
		expectCommunityFenceWithArgs(mock, teamID, spaceID, 3)
		mock.ExpectQuery(regexp.QuoteMeta("WITH stale AS")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow("bad"))
		store := NewStore(db, communityPassthroughRLS{})
		_, err := store.RefreshCommunityStaleness(context.Background(), CommunityStalenessInput{TeamID: teamID})
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
