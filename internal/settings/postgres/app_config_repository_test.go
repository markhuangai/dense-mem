package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func newSettingsMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(postgresdriver.New(postgresdriver.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock, func() { _ = sqlDB.Close() }
}

func TestAppConfigRepositoryReadsUpdateTimeInsideSystemTransaction(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value\n\t\t\t\tFROM app_config\n\t\t\t\tWHERE key = $1")).
		WithArgs(domain.AppConfigUpdateTimeKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("v1"))
	mock.ExpectCommit()

	value, err := repo.GetUpdateTime(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v1", value)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryUpdatesValuesInSortedOrderAndRefreshesVersion(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO app_config").WithArgs("A_KEY", "a", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO app_config").WithArgs("Z_KEY", "z", now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO app_config").WithArgs(domain.AppConfigUpdateTimeKey, "v2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	changed, err := repo.UpdateValues(context.Background(), map[string]string{"Z_KEY": "z", "A_KEY": "a"}, "v2", now)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryListsEntries(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)
	now := time.Date(2026, 9, 9, 3, 4, 5, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT key, value, updated_at\n\t\t\tFROM app_config\n\t\t\tORDER BY key ASC")).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value", "updated_at"}).
			AddRow("A_KEY", "a", now).
			AddRow("Z_KEY", "z", now.Add(time.Minute)))
	mock.ExpectCommit()

	entries, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, domain.AppConfigEntry{Key: "A_KEY", Value: "a", UpdatedAt: now}, entries["A_KEY"])
	require.Equal(t, domain.AppConfigEntry{Key: "Z_KEY", Value: "z", UpdatedAt: now.Add(time.Minute)}, entries["Z_KEY"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryReturnsWrappedDatabaseErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value\n\t\t\t\tFROM app_config\n\t\t\t\tWHERE key = $1")).
		WithArgs(domain.AppConfigUpdateTimeKey).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err := repo.GetUpdateTime(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get app config update time")
	require.ErrorContains(t, err, "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryRejectsMissingUpdateTime(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value\n\t\t\t\tFROM app_config\n\t\t\t\tWHERE key = $1")).
		WithArgs(domain.AppConfigUpdateTimeKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}))
	mock.ExpectRollback()

	_, err := repo.GetUpdateTime(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get app config update time")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryReturnsScanErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value\n\t\t\t\tFROM app_config\n\t\t\t\tWHERE key = $1")).
		WithArgs(domain.AppConfigUpdateTimeKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(nil))
	mock.ExpectRollback()

	_, err := repo.GetUpdateTime(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get app config update time")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryReturnsListQueryErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT key, value, updated_at\n\t\t\tFROM app_config\n\t\t\tORDER BY key ASC")).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err := repo.List(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to list app config")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryReturnsListScanErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT key, value, updated_at\n\t\t\tFROM app_config\n\t\t\tORDER BY key ASC")).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value", "updated_at"}).AddRow("A_KEY", nil, time.Now().UTC()))
	mock.ExpectRollback()

	_, err := repo.List(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to list app config")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositorySkipsVersionWhenValuesDoNotChange(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)
	now := time.Date(2026, 9, 9, 3, 4, 5, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO app_config").WithArgs("A_KEY", "a", now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	changed, err := repo.UpdateValues(context.Background(), map[string]string{"A_KEY": "a"}, "v2", now)
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppConfigRepositoryReturnsUpdateExecErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewAppConfigRepository(db, nil)
	now := time.Date(2026, 9, 9, 3, 4, 5, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO app_config").WithArgs("A_KEY", "a", now).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	changed, err := repo.UpdateValues(context.Background(), map[string]string{"A_KEY": "a"}, "v2", now)
	require.Error(t, err)
	require.False(t, changed)
	require.ErrorContains(t, err, "failed to update app config")
	require.NoError(t, mock.ExpectationsWereMet())
}
