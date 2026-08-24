package repository

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRequireRelationshipVersionDistinguishesMismatchFromDatabaseFailure(t *testing.T) {
	for _, test := range []struct {
		name         string
		rows         *sqlmock.Rows
		databaseErr  error
		wantMismatch bool
	}{
		{
			name:         "missing version is a typed mismatch",
			rows:         sqlmock.NewRows([]string{"exists"}).AddRow(false),
			wantMismatch: true,
		},
		{
			name:        "query failure remains operational",
			databaseErr: errors.New("database unavailable"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, err)

			expectation := mock.ExpectQuery(`SELECT EXISTS`).WithArgs(
				"00000000-0000-0000-0000-000000000001",
				"00000000-0000-0000-0000-000000000002",
				3,
			)
			if test.databaseErr != nil {
				expectation.WillReturnError(test.databaseErr)
			} else {
				expectation.WillReturnRows(test.rows)
			}

			err = requireRelationshipVersion(
				t.Context(), db,
				"00000000-0000-0000-0000-000000000001",
				"00000000-0000-0000-0000-000000000002",
				"", 3,
			)
			if test.wantMismatch {
				require.ErrorIs(t, err, errRelationshipVersionMismatch)
			} else {
				require.ErrorIs(t, err, test.databaseErr)
				require.NotErrorIs(t, err, errRelationshipVersionMismatch)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
