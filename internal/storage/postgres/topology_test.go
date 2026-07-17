package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newTopologyMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn: sqlDB,
	}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock, func() { _ = sqlDB.Close() }
}

func expectTopologyQuery(mock sqlmock.Sqlmock, inRecovery bool, readOnly string, distributed bool, extension string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WillReturnRows(sqlmock.NewRows([]string{
			"in_recovery",
			"transaction_read_only",
			"distributed_extension",
			"distributed_extension_id",
		}).AddRow(inRecovery, readOnly, distributed, extension))
}

func TestValidateSinglePrimaryTopologyAcceptsWritablePrimary(t *testing.T) {
	db, mock, cleanup := newTopologyMockDB(t)
	defer cleanup()
	expectTopologyQuery(mock, false, "off", false, "")

	err := ValidateSinglePrimaryTopology(context.Background(), db)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateSinglePrimaryTopologyRejectsReadOnlyOrDistributed(t *testing.T) {
	cases := []struct {
		name        string
		inRecovery  bool
		readOnly    string
		distributed bool
		extension   string
	}{
		{name: "in recovery", inRecovery: true, readOnly: "off"},
		{name: "transaction read only", readOnly: "on"},
		{name: "distributed extension", readOnly: "off", distributed: true, extension: "citus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, cleanup := newTopologyMockDB(t)
			defer cleanup()
			expectTopologyQuery(mock, tc.inRecovery, tc.readOnly, tc.distributed, tc.extension)

			err := ValidateSinglePrimaryTopology(context.Background(), db)

			require.Error(t, err)
			require.True(t, errors.Is(err, ErrUnsupportedPostgresTopology), "err=%v", err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCheckPGVectorExtension(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		db, mock, cleanup := newTopologyMockDB(t)
		defer cleanup()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		err := CheckPGVectorExtension(context.Background(), db)

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("missing", func(t *testing.T) {
		db, mock, cleanup := newTopologyMockDB(t)
		defer cleanup()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		err := CheckPGVectorExtension(context.Background(), db)

		require.ErrorContains(t, err, "vector extension is not installed")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
