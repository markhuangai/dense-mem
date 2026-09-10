package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUsageMetricsRepositoryWritesAndPrunesBuckets(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	bucket := domainUsageMetricBucket(now)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO usage_metric_buckets")).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), bucket.Route, bucket.Method, bucket.StatusClass,
		bucket.RequestCount, bucket.ErrorCount, bucket.TotalLatencyMS, bucket.MaxLatencyMS, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))

	repo := NewUsageMetricsRepository(db, passthroughRLS{})
	require.NoError(t, repo.UpsertBuckets(context.Background(), []domain.UsageMetricBucket{bucket}))
	require.NoError(t, repo.UpsertBuckets(context.Background(), nil))

	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM usage_metric_buckets WHERE bucket_start < $1")).WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.PruneBefore(context.Background(), now))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageMetricsRepositoryReadsSnapshotAndCalculatesTotals(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	teamID := uuid.New()
	keyID := uuid.New()

	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"coalesce", "coalesce", "coalesce", "coalesce"}).AddRow(int64(10), int64(2), int64(500), int64(80)))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"team_id", "team_name", "requests", "errors", "total_latency", "max_latency"}).AddRow(teamID.String(), "Team", int64(10), int64(2), int64(500), int64(80)))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"team_id", "team_name", "key_id", "key_name", "key_suffix", "requests", "errors", "total_latency", "max_latency"}).AddRow(teamID.String(), "Team", keyID.String(), "Key", "suffix", int64(10), int64(2), int64(500), int64(80)))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"route", "method", "status_class", "requests", "errors", "total_latency", "max_latency"}).AddRow("/mcp", "POST", 2, int64(10), int64(2), int64(500), int64(80)))

	repo := NewUsageMetricsRepository(db, passthroughRLS{})
	snapshot, err := repo.Snapshot(context.Background(), domain.UsageMetricsFilter{From: from, To: to, TeamID: &teamID})
	require.NoError(t, err)
	require.EqualValues(t, 10, snapshot.System.Requests)
	require.EqualValues(t, 2, snapshot.System.Errors)
	require.Equal(t, 50.0, snapshot.System.AvgLatencyMS)
	require.Len(t, snapshot.Teams, 1)
	require.Len(t, snapshot.Keys, 1)
	require.Len(t, snapshot.Routes, 1)
	require.Equal(t, "2xx", snapshot.Routes[0].StatusClass)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageMetricsRepositoryReportsMalformedRows(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"coalesce", "coalesce", "coalesce", "coalesce"}).AddRow(int64(1), int64(0), int64(1), int64(1)))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"team_id", "team_name", "requests", "errors", "total_latency", "max_latency"}).AddRow("not-a-uuid", "Team", int64(1), int64(0), int64(1), int64(1)))

	repo := NewUsageMetricsRepository(db, passthroughRLS{})
	_, err := repo.Snapshot(context.Background(), domain.UsageMetricsFilter{})
	require.ErrorContains(t, err, "invalid UUID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageMetricsRepositoryUsesTransactionWhenRLSIsUnavailable(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	repo := NewUsageMetricsRepository(db, nil)
	called := false
	require.NoError(t, repo.withSystemTx(context.Background(), func(tx *gorm.DB) error {
		called = tx != nil
		return nil
	}))
	require.True(t, called)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageMetricsRepositoryHelpersHandleNullableTotals(t *testing.T) {
	require.EqualValues(t, 7, nullInt64(sql.NullInt64{Int64: 7, Valid: true}))
	require.Zero(t, nullInt64(sql.NullInt64{}))
	require.Equal(t, domain.UsageMetricTotal{Requests: 0, MaxLatencyMS: 20}, totalFromSums(0, 0, 10, 20))
}

func domainUsageMetricBucket(now time.Time) domain.UsageMetricBucket {
	return domain.UsageMetricBucket{
		BucketStart:    now,
		TeamID:         uuid.New(),
		KeyID:          uuid.New(),
		Route:          "/mcp",
		Method:         "POST",
		StatusClass:    2,
		RequestCount:   10,
		ErrorCount:     2,
		TotalLatencyMS: 500,
		MaxLatencyMS:   80,
		LastSeenAt:     now,
	}
}
