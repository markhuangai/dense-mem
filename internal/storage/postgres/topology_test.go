package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestValidateSinglePrimaryTopologyAcceptsWritablePrimary(t *testing.T) {
	db, mock, cleanup := newTopologyMockDB(t)
	defer cleanup()

	expectTopologyQuery(mock, false, "off", false, "")

	err := ValidateSinglePrimaryTopology(context.Background(), db)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateSinglePrimaryTopologyRejectsReadOnlyOrRecovery(t *testing.T) {
	cases := []struct {
		name       string
		inRecovery bool
		readOnly   string
	}{
		{name: "standby", inRecovery: true, readOnly: "off"},
		{name: "read only", inRecovery: false, readOnly: "on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, cleanup := newTopologyMockDB(t)
			defer cleanup()

			expectTopologyQuery(mock, tc.inRecovery, tc.readOnly, false, "")

			err := ValidateSinglePrimaryTopology(context.Background(), db)
			require.ErrorIs(t, err, ErrUnsupportedPostgresTopology)
			require.ErrorContains(t, err, "read-only or in recovery")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestValidateSinglePrimaryTopologyRejectsDistributedExtension(t *testing.T) {
	db, mock, cleanup := newTopologyMockDB(t)
	defer cleanup()

	expectTopologyQuery(mock, false, "off", true, "citus")

	err := ValidateSinglePrimaryTopology(context.Background(), db)
	require.ErrorIs(t, err, ErrUnsupportedPostgresTopology)
	require.ErrorContains(t, err, `distributed postgres extension "citus"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDetectTopologyPropagatesQueryError(t *testing.T) {
	db, mock, cleanup := newTopologyMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT").WillReturnError(errors.New("query failed"))

	_, err := DetectTopology(context.Background(), db)
	require.ErrorContains(t, err, "query failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectTopologyQuery(mock sqlmock.Sqlmock, inRecovery bool, readOnly string, distributed bool, ext string) {
	mock.ExpectQuery("SELECT").
		WillReturnRows(sqlmock.NewRows([]string{
			"in_recovery",
			"transaction_read_only",
			"distributed_extension",
			"distributed_extension_id",
		}).AddRow(inRecovery, readOnly, distributed, ext))
}

func newTopologyMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	return db, mock, func() {
		_ = sqlDB.Close()
	}
}
