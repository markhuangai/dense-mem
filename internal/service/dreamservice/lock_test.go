package dreamservice

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresCycleLockerDoesNotApplyLockTimeoutToWork(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("dreaming:profile-1:2026-06-11").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err = NewPostgresCycleLocker().WithCycleLock(context.Background(), db, "profile-1", "2026-06-11", 5*time.Millisecond, func(tx *gorm.DB) error {
		time.Sleep(20 * time.Millisecond)
		return tx.Statement.Context.Err()
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
