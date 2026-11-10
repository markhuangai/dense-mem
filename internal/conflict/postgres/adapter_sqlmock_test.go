package postgres_test

import (
	"context"

	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/stretchr/testify/require"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
)

func TestGetEvidenceConflictHydratesEventsAndCursorWithSQLMock(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	}()
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
}
