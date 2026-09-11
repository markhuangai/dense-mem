package postgres_test

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestListConflictQueueHydratesProjectionAndSummaryWithSQLMock(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer db.Close()
	store := conflictpostgres.NewStore(gormDB, conflictSQLMockRLS{}, nil)
	teamID := uuid.NewString()
	conflictID := uuid.NewString()
	positionID := uuid.NewString()
	profileID := uuid.NewString()
	collectedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	reviewDueAt := collectedAt.Add(time.Hour)
	createdAt := collectedAt.Add(-time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"transaction_timestamp"}).AddRow(collectedAt))
	mock.ExpectQuery("FROM relationship_conflict_cases").
		WillReturnRows(sqlmock.NewRows([]string{"open", "overdue", "active", "expired", "oldest_open", "oldest_overdue"}).AddRow(1, 0, 1, 0, int64(3600), int64(0)))
	mock.ExpectQuery("FROM relationship_conflict_ai_assessment_events").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM relationship_conflict_resolution_plans").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM relationship_conflict_derived_evidence_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"pending", "failed"}).AddRow(1, 0))
	mock.ExpectQuery("SELECT team_id::text, conflict_id::text, semantic_scope_key").
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "conflict_id", "semantic_scope_key", "kind", "status", "subject_entity_id", "predicate_key",
			"predicate_version", "relationship_kind", "current_cardinality", "polarity", "scope_key", "question",
			"policy_version", "review_due_at", "next_review_at", "review_ttl_days", "timezone", "preferred_position_id",
			"resolved_at", "effective_at", "effective_time_basis", "resolution_reason", "version", "attempts", "created_at",
			"updated_at", "lease_until", "failure_class",
		}).AddRow(teamID, conflictID, "scope", "relationship", "open", uuid.NewString(), "predicate", 1, "state", "one", "+", "scope", "question", "policy", reviewDueAt, reviewDueAt, 5, "UTC", "", nil, nil, "", "", 1, 2, createdAt, collectedAt, collectedAt.Add(time.Minute), ""))
	mock.ExpectQuery("SELECT team_id::text, conflict_id::text, COALESCE\\(space_id::text").
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "conflict_id", "space_id", "semantic_scope_key", "kind", "status", "subject_entity_id", "predicate_key",
			"predicate_version", "relationship_kind", "current_cardinality", "polarity", "scope_key", "question", "policy_version",
			"review_due_at", "next_review_at", "review_ttl_days", "timezone", "preferred_position_id", "resolved_at", "effective_at",
			"effective_time_basis", "resolution_reason", "version", "attempts", "created_at", "updated_at", "dismissed_at",
		}).AddRow(teamID, conflictID, uuid.NewString(), "scope", "relationship", "open", uuid.NewString(), "predicate", 1, "state", "one", "+", "scope", "question", "policy", reviewDueAt, reviewDueAt, 5, "UTC", "", nil, nil, "", "", 1, 2, createdAt, collectedAt, nil))
	mock.ExpectQuery("WITH grouped AS").
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_id", "position_id", "position_key", "object_entity_id", "object_value_id", "disposition",
			"relationship_ids", "owner_profile_ids", "evidence_ids", "effective_at", "effective_time_basis", "recorded_fallback", "position_count",
		}).AddRow(conflictID, positionID, "position", uuid.NewString(), "", "candidate", pq.Array([]string{}), pq.Array([]string{profileID}), pq.Array([]string{uuid.NewString()}), nil, "", false, 1))
	mock.ExpectQuery("ranked_supporters").
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_id", "position_id", "supporter_count", "owner_profile_id", "display_name", "authority", "fragment_id", "accepted_at",
		}).AddRow(conflictID, positionID, 1, profileID, "Profile", "primary", uuid.NewString(), collectedAt))

	page, err := store.ListConflictQueue(context.Background(), domain.ConflictQueueQuery{TeamID: teamID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, conflictID, page.Items[0].ConflictID)
	require.Equal(t, "active", page.Items[0].LeaseState)
	require.Len(t, page.Items[0].Positions, 1)
	require.Len(t, page.Items[0].Positions[0].Supporters, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListConflictQueuePaginatesWithStableCursorAndNoDuplicates(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer db.Close()
	store := conflictpostgres.NewStore(gormDB, conflictSQLMockRLS{}, nil)
	teamID := uuid.NewString()
	firstID := uuid.NewString()
	secondID := uuid.NewString()
	collectedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	firstNextReviewAt := collectedAt.Add(time.Hour)
	secondNextReviewAt := collectedAt.Add(2 * time.Hour)

	expectQueuePage := func(rows ...[]driver.Value) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).
			WillReturnRows(sqlmock.NewRows([]string{"transaction_timestamp"}).AddRow(collectedAt))
		mock.ExpectQuery("FROM relationship_conflict_cases").
			WillReturnRows(sqlmock.NewRows([]string{"open", "overdue", "active", "expired", "oldest_open", "oldest_overdue"}).AddRow(2, 0, 0, 0, int64(7200), int64(0)))
		mock.ExpectQuery("FROM relationship_conflict_ai_assessment_events").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery("FROM relationship_conflict_resolution_plans").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery("FROM relationship_conflict_derived_evidence_tasks").
			WillReturnRows(sqlmock.NewRows([]string{"pending", "failed"}).AddRow(0, 0))
		pageRows := sqlmock.NewRows(conflictQueuePageColumns())
		for _, row := range rows {
			pageRows.AddRow(row...)
		}
		mock.ExpectQuery(`SELECT team_id::text, conflict_id::text, semantic_scope_key[\s\S]*ORDER BY relationship_conflict_cases.status DESC,\s+relationship_conflict_cases.next_review_at,\s+relationship_conflict_cases.conflict_id\s+LIMIT \$[0-9]+`).
			WillReturnRows(pageRows)
		caseRows := sqlmock.NewRows(conflictQueueHydrationColumns())
		for _, row := range rows {
			caseRows.AddRow(conflictQueueHydrationRow(row)...)
		}
		mock.ExpectQuery(`SELECT team_id::text, conflict_id::text, COALESCE\(space_id::text`).
			WillReturnRows(caseRows)
		mock.ExpectQuery("WITH grouped AS").
			WillReturnRows(sqlmock.NewRows([]string{
				"conflict_id", "position_id", "position_key", "object_entity_id", "object_value_id", "disposition",
				"relationship_ids", "owner_profile_ids", "evidence_ids", "effective_at", "effective_time_basis", "recorded_fallback", "position_count",
			}))
	}

	expectQueuePage(
		conflictQueueRow(teamID, firstID, firstNextReviewAt, collectedAt.Add(-2*time.Hour)),
		conflictQueueRow(teamID, secondID, secondNextReviewAt, collectedAt.Add(-time.Hour)),
	)
	firstPage, err := store.ListConflictQueue(context.Background(), domain.ConflictQueueQuery{TeamID: teamID, Status: "open", Limit: 1})
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 1)
	require.Equal(t, firstID, firstPage.Items[0].ConflictID)
	require.NotNil(t, firstPage.NextCursor)
	firstCursor, err := domain.DecodeConflictQueueCursor(*firstPage.NextCursor)
	require.NoError(t, err)
	require.Equal(t, teamID, firstCursor.TeamID)
	require.Equal(t, "open", firstCursor.StatusFilter)
	require.Equal(t, "open", firstCursor.Status)
	require.Equal(t, firstNextReviewAt, firstCursor.NextReviewAt)
	require.Equal(t, firstID, firstCursor.ConflictID)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"transaction_timestamp"}).AddRow(collectedAt))
	mock.ExpectQuery("FROM relationship_conflict_cases").
		WillReturnRows(sqlmock.NewRows([]string{"open", "overdue", "active", "expired", "oldest_open", "oldest_overdue"}).AddRow(1, 0, 0, 0, int64(3600), int64(0)))
	mock.ExpectQuery("FROM relationship_conflict_ai_assessment_events").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM relationship_conflict_resolution_plans").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM relationship_conflict_derived_evidence_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"pending", "failed"}).AddRow(0, 0))
	secondPageRows := sqlmock.NewRows(conflictQueuePageColumns()).AddRow(conflictQueueRow(teamID, secondID, secondNextReviewAt, collectedAt.Add(-time.Hour))...)
	mock.ExpectQuery(`SELECT team_id::text, conflict_id::text, semantic_scope_key[\s\S]*ORDER BY relationship_conflict_cases.status DESC,\s+relationship_conflict_cases.next_review_at,\s+relationship_conflict_cases.conflict_id\s+LIMIT \$[0-9]+`).
		WithArgs(teamID, "open", "open", "open", firstNextReviewAt, firstNextReviewAt, firstID, 2).
		WillReturnRows(secondPageRows)
	secondCaseRows := sqlmock.NewRows(conflictQueueHydrationColumns()).AddRow(conflictQueueHydrationRow(conflictQueueRow(teamID, secondID, secondNextReviewAt, collectedAt.Add(-time.Hour)))...)
	mock.ExpectQuery(`SELECT team_id::text, conflict_id::text, COALESCE\(space_id::text`).
		WillReturnRows(secondCaseRows)
	mock.ExpectQuery("WITH grouped AS").
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_id", "position_id", "position_key", "object_entity_id", "object_value_id", "disposition",
			"relationship_ids", "owner_profile_ids", "evidence_ids", "effective_at", "effective_time_basis", "recorded_fallback", "position_count",
		}))

	secondPage, err := store.ListConflictQueue(context.Background(), domain.ConflictQueueQuery{TeamID: teamID, Status: "open", Limit: 1, Cursor: firstCursor})
	require.NoError(t, err)
	require.Len(t, secondPage.Items, 1)
	require.Equal(t, secondID, secondPage.Items[0].ConflictID)
	require.Nil(t, secondPage.NextCursor)
	require.NoError(t, mock.ExpectationsWereMet())
}

func conflictQueuePageColumns() []string {
	return []string{
		"team_id", "conflict_id", "semantic_scope_key", "kind", "status", "subject_entity_id", "predicate_key",
		"predicate_version", "relationship_kind", "current_cardinality", "polarity", "scope_key", "question",
		"policy_version", "review_due_at", "next_review_at", "review_ttl_days", "timezone", "preferred_position_id",
		"resolved_at", "effective_at", "effective_time_basis", "resolution_reason", "version", "attempts", "created_at",
		"updated_at", "lease_until", "failure_class",
	}
}

func conflictQueueHydrationColumns() []string {
	return []string{
		"team_id", "conflict_id", "space_id", "semantic_scope_key", "kind", "status", "subject_entity_id", "predicate_key",
		"predicate_version", "relationship_kind", "current_cardinality", "polarity", "scope_key", "question", "policy_version",
		"review_due_at", "next_review_at", "review_ttl_days", "timezone", "preferred_position_id", "resolved_at", "effective_at",
		"effective_time_basis", "resolution_reason", "version", "attempts", "created_at", "updated_at", "dismissed_at",
	}
}

func conflictQueueRow(teamID, conflictID string, nextReviewAt, createdAt time.Time) []driver.Value {
	return []driver.Value{
		teamID, conflictID, "scope", "relationship", "open", uuid.NewString(), "predicate", 1, "state", "one", "+", "scope",
		"question", "policy", nextReviewAt.Add(-time.Hour), nextReviewAt, 5, "UTC", "", nil, nil, "", "", 1, 0,
		createdAt, nextReviewAt, nil, "",
	}
}

func conflictQueueHydrationRow(pageRow []driver.Value) []driver.Value {
	row := []driver.Value{pageRow[0], pageRow[1], uuid.NewString()}
	row = append(row, pageRow[2:27]...)
	return append(row, nil)
}

func TestGetEvidenceConflictHydratesEventsAndCursorWithSQLMock(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer db.Close()
	store := conflictpostgres.NewStore(gormDB, conflictListSQLMockRLS{}, nil)
	teamID := uuid.NewString()
	conflictID := uuid.NewString()
	updatedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT team_id::text, conflict_id::text, space_id::text").
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "conflict_id", "space_id", "space_generation", "status", "version", "preferred_position_id",
			"resolved_at", "resolution_reason", "created_at", "updated_at",
		}).AddRow(teamID, conflictID, uuid.NewString(), 1, "open", 2, "", nil, "", updatedAt, updatedAt))
	mock.ExpectQuery("SELECT conflict_id::text, position_id::text").
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_id", "position_id", "position_key", "canonical_evidence_id", "canonical_owner_profile_id", "occurrence_id",
			"occurrence_owner_profile_id", "quote", "span_start", "span_end", "authority", "submitted", "created_at",
		}).AddRow(conflictID, uuid.NewString(), "position", uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), "quote", 0, 5, "primary", true, updatedAt))
	mock.ExpectQuery("SELECT conflict_event_id::text").
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_event_id", "conflict_id", "ordinal", "action", "status_after", "case_version", "actor_kind", "actor_id", "reason", "preferred_position_id", "citation_snapshot", "created_at",
		}).AddRow(uuid.NewString(), conflictID, 2, "created", "open", 2, "system", "", "created", "", []byte("[]"), updatedAt).
			AddRow(uuid.NewString(), conflictID, 1, "opened", "open", 1, "system", "", "opened", "", []byte("[]"), updatedAt.Add(-time.Minute)))

	result, err := store.GetEvidenceConflict(context.Background(), conflictpostgres.EvidenceConflictGetInput{TeamID: teamID, ConflictID: conflictID, EventLimit: 1})
	require.NoError(t, err)
	require.NotNil(t, result.Conflict)
	require.Len(t, result.Conflict.Events, 1)
	require.NotNil(t, result.NextEventCursor)
	require.NoError(t, mock.ExpectationsWereMet())
}
