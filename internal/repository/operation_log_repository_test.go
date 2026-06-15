package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestScanOperationLogRejectsMalformedAttrsJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"id",
		"timestamp",
		"severity",
		"severity_rank",
		"message",
		"source",
		"team_id",
		"profile_id",
		"correlation_id",
		"error",
		"attrs",
	}).AddRow(
		uuid.New().String(),
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		"INFO",
		20,
		"message",
		"source",
		nil,
		nil,
		"corr-1",
		"",
		[]byte("{bad-json"),
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	defer sqlRows.Close()
	require.True(t, sqlRows.Next())

	_, err = scanOperationLog(sqlRows)
	require.ErrorContains(t, err, "invalid operation_logs.attrs JSON")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanOperationLogRejectsMalformedID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"id",
		"timestamp",
		"severity",
		"severity_rank",
		"message",
		"source",
		"team_id",
		"profile_id",
		"correlation_id",
		"error",
		"attrs",
	}).AddRow(
		"not-a-uuid",
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		"INFO",
		20,
		"message",
		"source",
		nil,
		nil,
		"corr-1",
		"",
		[]byte("{}"),
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	defer sqlRows.Close()
	require.True(t, sqlRows.Next())

	_, err = scanOperationLog(sqlRows)
	require.ErrorContains(t, err, "invalid operation_logs.id UUID")
	require.NoError(t, mock.ExpectationsWereMet())
}
