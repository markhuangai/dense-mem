package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestReadPlacementRunClosesRunRowsBeforeReadingItems(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 2, 30, 0, 0, time.UTC)
	runRows := sqlmock.NewRows([]string{
		"ingest_id",
		"profile_id",
		"status",
		"check_after_seconds",
		"status_tool",
		"evidence",
		"error",
		"created_at",
		"updated_at",
		"started_at",
		"completed_at",
	}).AddRow(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"completed",
		60,
		"get_memory_placement",
		[]byte(`[{"index":0,"content":"memory"}]`),
		"",
		now,
		now,
		nil,
		now,
	)
	itemRows := sqlmock.NewRows([]string{
		"item_id",
		"ingest_id",
		"profile_id",
		"evidence_index",
		"fragment_id",
		"category",
		"status",
		"reason",
		"error",
		"claim_id",
		"fact_id",
		"created_at",
		"updated_at",
	}).AddRow(
		"33333333-3333-3333-3333-333333333333",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		0,
		"fragment-1",
		"fragment_only",
		"completed",
		"stored as fragment",
		"",
		"",
		"",
		now,
		now,
	)

	mock.ExpectQuery(`SELECT ingest_id::text, profile_id::text, status`).
		WithArgs("22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111").
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`SELECT item_id::text, ingest_id::text, profile_id::text`).
		WithArgs("11111111-1111-1111-1111-111111111111").
		WillReturnRows(itemRows).
		RowsWillBeClosed()

	run, err := readPlacementRun(
		context.Background(),
		db,
		"WHERE profile_id = ? AND ingest_id = ?",
		"22222222-2222-2222-2222-222222222222",
		"11111111-1111-1111-1111-111111111111",
	)

	require.NoError(t, err)
	require.NotNil(t, run)
	require.Len(t, run.Items, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimNextQueuedRunCanReclaimStaleProcessingRun(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 2, 45, 0, 0, time.UTC)
	ingestID := "11111111-1111-1111-1111-111111111111"
	profileID := "22222222-2222-2222-2222-222222222222"
	runRows := sqlmock.NewRows([]string{
		"ingest_id",
		"profile_id",
		"status",
		"check_after_seconds",
		"status_tool",
		"evidence",
		"error",
		"created_at",
		"updated_at",
		"started_at",
		"completed_at",
	}).AddRow(
		ingestID,
		profileID,
		"processing",
		60,
		"get_memory_placement",
		[]byte(`[{"index":0,"content":"memory"}]`),
		"",
		now.Add(-10*time.Minute),
		now,
		now,
		nil,
	)
	itemRows := sqlmock.NewRows([]string{
		"item_id",
		"ingest_id",
		"profile_id",
		"evidence_index",
		"fragment_id",
		"category",
		"status",
		"reason",
		"error",
		"claim_id",
		"fact_id",
		"created_at",
		"updated_at",
	})

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH next AS.*status = 'queued'.*status = 'processing' AND updated_at < now\(\) - interval '5 minutes'.*RETURNING run\.ingest_id::text`).
		WillReturnRows(sqlmock.NewRows([]string{"ingest_id"}).AddRow(ingestID)).
		RowsWillBeClosed()
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ingest_id::text, profile_id::text, status`).
		WithArgs(ingestID).
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`SELECT item_id::text, ingest_id::text, profile_id::text`).
		WithArgs(ingestID).
		WillReturnRows(itemRows).
		RowsWillBeClosed()
	mock.ExpectCommit()

	repo := NewMemoryPlacementRepository(db, nil)
	run, err := repo.ClaimNextQueuedRun(context.Background())

	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, ingestID, run.IngestID)
	require.Equal(t, profileID, run.ProfileID)
	require.NoError(t, mock.ExpectationsWereMet())
}
