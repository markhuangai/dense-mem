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

func TestSetRememberFailureArtifactHoldStateTxUsesCallerTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, sqlDB.Close())
	})
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	spaceID := uuid.New()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.remember_failure_artifact_retention_space_id', $1, true), set_config('app.remember_failure_artifact_retention_value', $2, true)")).
		WithArgs(spaceID.String(), "true").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE remember_failure_artifacts AS artifact")).
		WithArgs(true, spaceID, true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.remember_failure_artifact_retention_space_id', '', true), set_config('app.remember_failure_artifact_retention_value', '', true)")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, SetRememberFailureArtifactHoldStateTx(context.Background(), db, spaceID, true))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetRememberFailureArtifactHoldStateTxSkipsNilSpace(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, sqlDB.Close())
	})
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, SetRememberFailureArtifactHoldStateTx(context.Background(), db, uuid.Nil, true))
	require.NoError(t, mock.ExpectationsWereMet())
}
