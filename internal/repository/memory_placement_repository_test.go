package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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

func TestUpdateDisputeWithRunLocksAndSavesInOneTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 6, 27, 3, 0, 0, 0, time.UTC)
	disputeID := "44444444-4444-4444-4444-444444444444"
	ingestID := "11111111-1111-1111-1111-111111111111"
	profileID := "22222222-2222-2222-2222-222222222222"
	itemID := "33333333-3333-3333-3333-333333333333"

	disputeRows := sqlmock.NewRows([]string{
		"dispute_id",
		"profile_id",
		"ingest_id",
		"placement_item_id",
		"status",
		"turns",
		"final_reason",
		"created_at",
		"updated_at",
		"completed_at",
	}).AddRow(disputeID, profileID, ingestID, itemID, "open", []byte(`[]`), "", now, now, nil)
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
	}).AddRow(ingestID, profileID, "completed", 60, "get_memory_placement", []byte(`[]`), "", now, now, nil, nil)
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
	}).AddRow(itemID, ingestID, profileID, 0, "fragment-1", "fragment_only", "completed", "", "", "", "", now, now)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT dispute_id::text, profile_id::text, ingest_id::text.*FROM memory_dispute_sessions.*LIMIT 1 FOR UPDATE`).
		WithArgs(profileID, disputeID).
		WillReturnRows(disputeRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`(?s)SELECT ingest_id::text, profile_id::text, status.*FROM memory_placement_runs.*LIMIT 1 FOR UPDATE`).
		WithArgs(profileID, ingestID).
		WillReturnRows(runRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`(?s)SELECT item_id::text, ingest_id::text, profile_id::text.*FROM memory_placement_items.*FOR UPDATE`).
		WithArgs(ingestID).
		WillReturnRows(itemRows).
		RowsWillBeClosed()
	mock.ExpectExec(`UPDATE memory_dispute_sessions`).
		WithArgs(
			string(domain.MemoryDisputeOpen),
			sqlmock.AnyArg(),
			"updated",
			sqlmock.AnyArg(),
			nil,
			disputeID,
			profileID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE memory_placement_runs`).
		WithArgs(
			string(domain.MemoryPlacementCompleted),
			60,
			"get_memory_placement",
			sqlmock.AnyArg(),
			"",
			sqlmock.AnyArg(),
			nil,
			nil,
			ingestID,
			profileID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE memory_placement_items`).
		WithArgs(
			"fragment-1",
			string(domain.MemoryPlacementNeedsEvidence),
			"completed",
			"updated",
			"",
			"",
			"",
			sqlmock.AnyArg(),
			itemID,
			ingestID,
			profileID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewMemoryPlacementRepository(db, nil)
	session, run, err := repo.UpdateDisputeWithRun(context.Background(), profileID, disputeID, func(session *domain.MemoryDisputeSession, run *domain.MemoryPlacementRun) error {
		session.FinalReason = "updated"
		run.Items[0].Category = domain.MemoryPlacementNeedsEvidence
		run.Items[0].Reason = "updated"
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotNil(t, run)
	require.Equal(t, "updated", session.FinalReason)
	require.Equal(t, domain.MemoryPlacementNeedsEvidence, run.Items[0].Category)
	require.NoError(t, mock.ExpectationsWereMet())
}
