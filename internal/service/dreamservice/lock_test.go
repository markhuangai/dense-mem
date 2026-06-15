package dreamservice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresCycleLockerDoesNotApplyLockTimeoutToWork(t *testing.T) {
	type contextKey struct{}

	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("dreaming:profile-1:2026-06-11").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	parentCtx := context.WithValue(context.Background(), contextKey{}, "parent")
	called := false
	err = NewPostgresCycleLocker().WithCycleLock(parentCtx, db, "profile-1", "2026-06-11", time.Hour, func(tx *gorm.DB) error {
		called = true
		if got := tx.Statement.Context.Value(contextKey{}); got != "parent" {
			return fmt.Errorf("transaction context value = %v, want parent", got)
		}
		if _, ok := tx.Statement.Context.Deadline(); ok {
			return fmt.Errorf("transaction context should not inherit lock acquisition deadline")
		}
		return tx.Statement.Context.Err()
	})
	require.NoError(t, err)
	require.True(t, called)
	require.NoError(t, mock.ExpectationsWereMet())
}
