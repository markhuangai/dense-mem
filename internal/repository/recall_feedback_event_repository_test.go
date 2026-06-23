package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestScanRecallFeedbackEventRejectsMalformedToolArgsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{bad-json"), []byte("[]"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.tool_args JSON")
}

func TestScanRecallFeedbackEventRejectsMalformedResultRefsJSON(t *testing.T) {
	rows := recallFeedbackEventRows(t, []byte("{}"), []byte("{bad-json"))
	_, err := scanRecallFeedbackEvent(rows)
	require.ErrorContains(t, err, "invalid recall_feedback_events.result_refs JSON")
}

func recallFeedbackEventRows(t *testing.T, toolArgs []byte, resultRefs []byte) *sql.Rows {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rows := sqlmock.NewRows([]string{
		"recall_id",
		"created_at",
		"updated_at",
		"feedback_at",
		"team_id",
		"profile_id",
		"key_id",
		"auth_method",
		"tool_name",
		"query",
		"tool_args",
		"result_refs",
		"result_count",
		"snapshot_state",
		"used",
		"answer_supported",
		"quality",
		"missing_context",
		"irrelevant",
	}).AddRow(
		"rec_1",
		time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 23, 12, 1, 0, 0, time.UTC),
		nil,
		nil,
		nil,
		nil,
		"api_key",
		"recall_memory",
		"query",
		toolArgs,
		resultRefs,
		0,
		"captured",
		nil,
		nil,
		"",
		nil,
		nil,
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlRows.Close() })
	require.True(t, sqlRows.Next())
	require.NoError(t, mock.ExpectationsWereMet())
	return sqlRows
}
