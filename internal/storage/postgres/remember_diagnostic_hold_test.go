package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSetRememberAttemptDiagnosticHoldStateTxUsesCallerTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, sqlDB.Close())
	})
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	spaceID := uuid.New()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.remember_attempt_diagnostic_retention_space_id', $1, true)")).
		WithArgs(spaceID.String()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE remember_attempt_diagnostics AS diagnostic")).
		WithArgs(true, spaceID, true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.remember_attempt_diagnostic_retention_space_id', '', true)")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, SetRememberAttemptDiagnosticHoldStateTx(context.Background(), db, spaceID, true))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetRememberAttemptDiagnosticHoldStateTxSkipsNilSpace(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, sqlDB.Close())
	})
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, SetRememberAttemptDiagnosticHoldStateTx(context.Background(), db, uuid.Nil, true))
	require.NoError(t, mock.ExpectationsWereMet())
}
