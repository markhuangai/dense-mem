package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLoadTraceRelationshipPropagatesIteratorError(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	iteratorErr := errors.New("trace relationship iterator failed")
	mock.ExpectQuery(`SELECT r\.team_id`).
		WithArgs("team-id", "relationship-id").
		WillReturnRows(sqlmock.NewRows([]string{"relationship_id"}).AddRow("ignored").RowError(0, iteratorErr))

	_, err = loadTraceRelationship(context.Background(), db, "team-id", "relationship-id")
	require.ErrorIs(t, err, iteratorErr)
	require.NotErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}
