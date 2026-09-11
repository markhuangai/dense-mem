package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestConflictStoreCollectsQueueMetricsWithBoundedQueries(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer db.Close()
	store := conflictpostgres.NewStore(gormDB, conflictSQLMockRLS{}, nil)
	collectedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"transaction_timestamp"}).AddRow(collectedAt))
	mock.ExpectQuery("WITH relevant_teams").
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("team-a"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, status, COUNT(*)::double precision")).
		WillReturnRows(sqlmock.NewRows([]string{"team_id", "status", "count"}).AddRow("team-a", "open", 3.0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, status")).
		WithArgs(collectedAt).
		WillReturnRows(sqlmock.NewRows([]string{"team_id", "status", "age"}).AddRow("team-a", "open", 42.5))
	mock.ExpectQuery("SELECT team_id::text,\\s+CASE").
		WithArgs(collectedAt).
		WillReturnRows(sqlmock.NewRows([]string{"team_id", "lease_state", "count"}).AddRow("team-a", "active", 1.0))
	mock.ExpectQuery("SELECT team_id::text,\\s+CASE WHEN").
		WillReturnRows(sqlmock.NewRows([]string{"team_id", "status", "count"}).AddRow("team-a", "pending", 2.0))

	snapshot, err := store.CollectConflictQueueMetrics(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Cases, 2)
	require.Equal(t, domain.ConflictQueueMetricCase{TeamID: "team-a", Status: "open", Value: 3}, snapshot.Cases[0])
	require.Equal(t, domain.ConflictQueueMetricCase{TeamID: "team-a", Status: "overdue"}, snapshot.Cases[1])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConflictStoreCollectMetricsPropagatesQueryAndScanFailures(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		db, mock, gormDB := newConflictSQLMockDB(t)
		defer db.Close()
		store := conflictpostgres.NewStore(gormDB, conflictSQLMockRLS{}, nil)
		databaseErr := errors.New("queue metrics query failed")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).WillReturnError(databaseErr)

		_, err := store.CollectConflictQueueMetrics(context.Background())
		assert.ErrorIs(t, err, databaseErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		db, mock, gormDB := newConflictSQLMockDB(t)
		defer db.Close()
		store := conflictpostgres.NewStore(gormDB, conflictSQLMockRLS{}, nil)
		collectedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT transaction_timestamp()")).
			WillReturnRows(sqlmock.NewRows([]string{"transaction_timestamp"}).AddRow(collectedAt))
		mock.ExpectQuery("WITH relevant_teams").
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("team-a"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT team_id::text, status, COUNT(*)::double precision")).
			WillReturnRows(sqlmock.NewRows([]string{"team_id", "status", "count"}).AddRow("team-a", "open", "not-a-number"))

		_, err := store.CollectConflictQueueMetrics(context.Background())
		assert.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestListEvidenceConflictsHydratesOnlyRetainedPage(t *testing.T) {
	db, mock, gormDB := newConflictSQLMockDB(t)
	defer db.Close()
	store := conflictpostgres.NewStore(gormDB, conflictListSQLMockRLS{}, nil)
	teamID := "00000000-0000-0000-0000-000000000001"
	firstConflictID := "00000000-0000-0000-0000-000000000002"
	probeConflictID := "00000000-0000-0000-0000-000000000003"
	updatedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT team_id::text, conflict_id::text").
		WithArgs(teamID, "open", 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id", "conflict_id", "space_id", "space_generation", "status", "version",
			"preferred_position_id", "resolved_at", "resolution_reason", "created_at", "updated_at",
		}).
			AddRow(teamID, firstConflictID, "00000000-0000-0000-0000-000000000004", int64(1), "open", int64(1), "", nil, "", updatedAt, updatedAt).
			AddRow(teamID, probeConflictID, "00000000-0000-0000-0000-000000000004", int64(1), "open", int64(1), "", nil, "", updatedAt.Add(-time.Minute), updatedAt.Add(-time.Minute)))
	mock.ExpectQuery("SELECT conflict_id::text, position_id::text").
		WithArgs(teamID, firstConflictID).
		WillReturnRows(sqlmock.NewRows([]string{
			"conflict_id", "position_id", "position_key", "canonical_evidence_id", "canonical_owner_profile_id",
			"occurrence_id", "occurrence_owner_profile_id", "quote", "span_start", "span_end", "authority", "submitted", "created_at",
		}).AddRow(firstConflictID, "00000000-0000-0000-0000-000000000005", "position", "00000000-0000-0000-0000-000000000006", "00000000-0000-0000-0000-000000000007", "00000000-0000-0000-0000-000000000008", "00000000-0000-0000-0000-000000000009", "quote", int64(0), int64(5), "primary", true, updatedAt))

	result, err := store.ListEvidenceConflicts(context.Background(), conflictpostgres.EvidenceConflictListInput{TeamID: teamID, Status: "open", Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, firstConflictID, result.Items[0].ConflictID)
	require.Len(t, result.Items[0].Positions, 1)
	require.NotNil(t, result.NextCursor)
	require.NoError(t, mock.ExpectationsWereMet())
}

func newConflictSQLMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *gorm.DB) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	gormDB, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	return sqlDB, mock, gormDB
}

type conflictSQLMockRLS struct{}

var _ storagepostgres.RLSHelper = conflictSQLMockRLS{}

func (conflictSQLMockRLS) WithTeamTx(context.Context, *gorm.DB, string, func(*gorm.DB) error) error {
	return errors.New("unused RLS method")
}
func (conflictSQLMockRLS) WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error {
	return errors.New("unused RLS method")
}
func (conflictSQLMockRLS) WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error {
	return errors.New("unused RLS method")
}
func (conflictSQLMockRLS) WithTeamReadOnlyRepeatableTx(context.Context, *gorm.DB, string, func(*gorm.DB) error) error {
	return errors.New("unused RLS method")
}
func (conflictSQLMockRLS) WithTeamProfileReadOnlyRepeatableTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error {
	return errors.New("unused RLS method")
}
func (conflictSQLMockRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

type conflictListSQLMockRLS struct{ conflictSQLMockRLS }

func (conflictListSQLMockRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}
