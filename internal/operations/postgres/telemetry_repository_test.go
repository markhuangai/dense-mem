package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

func TestTelemetryLifecycleRepositoryReadsScopedSnapshots(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	teamID := uuid.New()
	profileID := uuid.New()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event.to_status")).WillReturnRows(sqlmock.NewRows([]string{"to_status", "count"}).AddRow("active", int64(2)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM relationship_correction_events")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT relationship.status")).WillReturnRows(sqlmock.NewRows([]string{"status", "count"}).AddRow("active", int64(3)))

	repo := NewTelemetryLifecycleRepository(db, passthroughRLS{})
	snapshot, err := repo.ReadTelemetryLifecycle(context.Background(), operationscontract.TelemetryLifecycleFilter{
		TeamID: &teamID, ProfileID: &profileID,
	}, from, to)
	require.NoError(t, err)
	require.Equal(t, 2.0, snapshot.Transitions["active"])
	require.Equal(t, 1.0, snapshot.Corrections)
	require.Equal(t, 3.0, snapshot.Current["active"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelemetryLifecycleRepositoryRejectsInvalidConfiguration(t *testing.T) {
	teamID := uuid.New()
	repo := NewTelemetryLifecycleRepository(nil, passthroughRLS{})
	_, err := repo.ReadTelemetryLifecycle(context.Background(), operationscontract.TelemetryLifecycleFilter{}, time.Time{}, time.Time{})
	require.ErrorContains(t, err, "reader is unavailable")

	repo = NewTelemetryLifecycleRepository(&gorm.DB{}, nil)
	_, err = repo.ReadTelemetryLifecycle(context.Background(), operationscontract.TelemetryLifecycleFilter{}, time.Time{}, time.Time{})
	require.ErrorContains(t, err, "reader is unavailable")

	repo = NewTelemetryLifecycleRepository(&gorm.DB{}, passthroughRLS{})
	_, err = repo.ReadTelemetryLifecycle(context.Background(), operationscontract.TelemetryLifecycleFilter{ProfileID: &teamID}, time.Time{}, time.Time{})
	require.ErrorContains(t, err, "profile scope requires a team")
}

func TestTelemetryLifecycleScopeClauseCoversSystemTeamAndProfileScopes(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()

	where, args := telemetryLifecycleScopeClause(operationscontract.TelemetryLifecycleFilter{}, "event")
	require.Empty(t, where)
	require.Empty(t, args)

	where, args = telemetryLifecycleScopeClause(operationscontract.TelemetryLifecycleFilter{TeamID: &teamID}, "event")
	require.Equal(t, " AND event.team_id = ?", where)
	require.Equal(t, []any{teamID.String()}, args)

	where, args = telemetryLifecycleScopeClause(operationscontract.TelemetryLifecycleFilter{TeamID: &teamID, ProfileID: &profileID}, "relationship")
	require.Equal(t, " AND relationship.team_id = ? AND relationship.owner_profile_id = ?", where)
	require.Equal(t, []any{teamID.String(), profileID.String()}, args)
}

func newOperationsMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *gorm.DB) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return sqlDB, mock, db
}

type passthroughRLS struct{}

func (passthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}
