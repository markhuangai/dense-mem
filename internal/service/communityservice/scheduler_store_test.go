package communityservice

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresSchedulerRunStore(t *testing.T) {
	db, mock, cleanup := newSchedulerRunStoreMockDB(t)
	defer cleanup()

	mock.ExpectExec(`(?s)INSERT INTO community_detection_runs.*ON CONFLICT \(profile_id, run_date\) DO NOTHING`).
		WithArgs("profile-1", "2026-06-15").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO community_detection_runs.*ON CONFLICT \(profile_id, run_date\) DO NOTHING`).
		WithArgs("profile-1", "2026-06-15").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)DELETE FROM community_detection_runs.*WHERE run_date < \$1`).
		WithArgs("2026-06-08").
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := NewPostgresSchedulerRunStore(db)
	reserved, err := store.TryMarkRun(context.Background(), "profile-1", "2026-06-15")
	require.NoError(t, err)
	require.True(t, reserved)

	reserved, err = store.TryMarkRun(context.Background(), "profile-1", "2026-06-15")
	require.NoError(t, err)
	require.False(t, reserved)

	require.NoError(t, store.Prune(context.Background(), "2026-06-08"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresSchedulerRunStoreErrors(t *testing.T) {
	store := NewPostgresSchedulerRunStore(nil)
	reserved, err := store.TryMarkRun(context.Background(), "profile-1", "2026-06-15")
	require.False(t, reserved)
	require.ErrorContains(t, err, "db is required")
	require.ErrorContains(t, store.Prune(context.Background(), "2026-06-08"), "db is required")

	db, mock, cleanup := newSchedulerRunStoreMockDB(t)
	defer cleanup()

	mock.ExpectExec(`(?s)INSERT INTO community_detection_runs.*ON CONFLICT \(profile_id, run_date\) DO NOTHING`).
		WithArgs("profile-1", "2026-06-15").
		WillReturnError(errors.New("insert failed"))
	mock.ExpectExec(`(?s)DELETE FROM community_detection_runs.*WHERE run_date < \$1`).
		WithArgs("2026-06-08").
		WillReturnError(errors.New("delete failed"))

	store = NewPostgresSchedulerRunStore(db)
	reserved, err = store.TryMarkRun(context.Background(), "profile-1", "2026-06-15")
	require.False(t, reserved)
	require.ErrorContains(t, err, "community scheduler run reserve")
	require.ErrorContains(t, store.Prune(context.Background(), "2026-06-08"), "community scheduler run prune")
	require.NoError(t, mock.ExpectationsWereMet())
}

func newSchedulerRunStoreMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock, func() {
		mock.ExpectClose()
		require.NoError(t, sqlDB.Close())
	}
}
