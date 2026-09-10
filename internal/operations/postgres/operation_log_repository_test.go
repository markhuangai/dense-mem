package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScanOperationLogRejectsMalformedAttrsJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"id",
		"timestamp",
		"severity",
		"severity_rank",
		"message",
		"source",
		"team_id",
		"profile_id",
		"correlation_id",
		"error",
		"attrs",
	}).AddRow(
		uuid.New().String(),
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		"INFO",
		20,
		"message",
		"source",
		nil,
		nil,
		"corr-1",
		"",
		[]byte("{bad-json"),
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	defer sqlRows.Close()
	require.True(t, sqlRows.Next())

	_, err = scanOperationLog(sqlRows)
	require.ErrorContains(t, err, "invalid operation_logs.attrs JSON")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanOperationLogRejectsMalformedID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"id",
		"timestamp",
		"severity",
		"severity_rank",
		"message",
		"source",
		"team_id",
		"profile_id",
		"correlation_id",
		"error",
		"attrs",
	}).AddRow(
		"not-a-uuid",
		time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		"INFO",
		20,
		"message",
		"source",
		nil,
		nil,
		"corr-1",
		"",
		[]byte("{}"),
	)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	sqlRows, err := db.QueryContext(context.Background(), "SELECT")
	require.NoError(t, err)
	defer sqlRows.Close()
	require.True(t, sqlRows.Next())

	_, err = scanOperationLog(sqlRows)
	require.ErrorContains(t, err, "invalid operation_logs.id UUID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationLogRepositoryAppendsListsAndPrunes(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	teamID := uuid.New()
	profileID := uuid.New()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO operation_logs")).WithArgs(
		now, "WARN", 30, "warning", "test", teamID.String(), profileID.String(), "corr", "", "{}",
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT\n\t\t\t\tid::text")).WillReturnRows(sqlmock.NewRows([]string{
		"id", "timestamp", "severity", "severity_rank", "message", "source", "team_id", "profile_id", "correlation_id", "error", "attrs",
	}).AddRow(uuid.New().String(), now, "WARN", 30, "warning", "test", teamID.String(), profileID.String(), "corr", "", []byte(`{"reference_type":"test"}`)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM operation_logs WHERE timestamp < $1")).WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 1))

	repo := NewOperationLogRepository(db, passthroughRLS{})
	require.NoError(t, repo.AppendBatch(context.Background(), []domain.OperationLog{{
		Timestamp: now, Severity: "WARN", SeverityRank: 30, Message: "warning", Source: "test",
		TeamID: &teamID, ProfileID: &profileID, CorrelationID: "corr", Attrs: map[string]any{},
	}}))
	page, err := repo.List(context.Background(), domain.OperationLogFilter{Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "warning", page.Items[0].Message)
	require.NoError(t, repo.PruneBefore(context.Background(), now))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationLogRepositoryHelpersNormalizeFiltersAndValues(t *testing.T) {
	from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("offset", 2*60*60))
	to := from.Add(time.Hour)
	teamID := uuid.New()
	normalized := normalizeOperationLogFilter(domain.OperationLogFilter{
		Limit:         999,
		Offset:        -1,
		Sort:          " SEVERITY ",
		Direction:     " ASC ",
		Severity:      " warn ",
		Event:         " event ",
		ReferenceType: " type ",
		ReferenceID:   " id ",
		TeamID:        &teamID,
		From:          &from,
		To:            &to,
	})
	require.Equal(t, 500, normalized.Limit)
	require.Equal(t, 0, normalized.Offset)
	require.Equal(t, "severity", normalized.Sort)
	require.Equal(t, "asc", normalized.Direction)
	require.Equal(t, "WARN", normalized.Severity)
	require.Equal(t, "event", normalized.Event)
	require.Equal(t, "type", normalized.ReferenceType)
	require.Equal(t, "id", normalized.ReferenceID)
	require.Equal(t, from.UTC(), *normalized.From)
	require.Equal(t, to.UTC(), *normalized.To)
	require.Equal(t, "ORDER BY severity_rank ASC, timestamp DESC, id DESC", operationLogOrderClause(normalized))

	defaults := normalizeOperationLogFilter(domain.OperationLogFilter{Limit: 0, Sort: "other", Direction: "other"})
	require.Equal(t, 100, defaults.Limit)
	require.Equal(t, "timestamp", defaults.Sort)
	require.Equal(t, "desc", defaults.Direction)
	require.Equal(t, "ORDER BY timestamp DESC, id DESC", operationLogOrderClause(defaults))
	require.Equal(t, "INFO", normalizeOperationLogSeverity(" "))
	require.Equal(t, "ERROR", normalizeOperationLogSeverity(" error "))
	require.Equal(t, map[string]any{}, nonNilMap(nil))
	require.Nil(t, timePtrValue(nil))
	require.Equal(t, from.UTC(), timePtrValue(&from))
	require.Nil(t, parseNullableUUID(sql.NullString{}))
	require.Nil(t, parseNullableUUID(sql.NullString{Valid: true, String: "not-a-uuid"}))
	require.NotNil(t, parseNullableUUID(sql.NullString{Valid: true, String: teamID.String()}))
}

func TestOperationLogRepositoryReportsWriteAndPruneErrors(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	repo := NewOperationLogRepository(db, passthroughRLS{})
	entry := domain.OperationLog{Timestamp: time.Now().UTC(), Severity: "INFO", Message: "failed"}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO operation_logs")).WillReturnError(errors.New("insert failed"))
	require.ErrorContains(t, repo.AppendBatch(context.Background(), []domain.OperationLog{entry}), "failed to append operation logs")
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM operation_logs WHERE timestamp < $1")).WillReturnError(errors.New("delete failed"))
	require.ErrorContains(t, repo.PruneBefore(context.Background(), entry.Timestamp), "failed to prune operation logs")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationLogRepositoryReportsListQueryErrors(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*)")).WillReturnError(errors.New("count failed"))

	_, err := NewOperationLogRepository(db, passthroughRLS{}).List(context.Background(), domain.OperationLogFilter{})
	require.ErrorContains(t, err, "failed to list operation logs")
	require.ErrorContains(t, err, "count failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationLogRepositoryHandlesEmptyBatchAndNoRLS(t *testing.T) {
	sqlDB, mock, db := newOperationsMockDB(t)
	defer sqlDB.Close()
	repo := NewOperationLogRepository(db, nil)
	require.NoError(t, repo.AppendBatch(context.Background(), nil))
	mock.ExpectBegin()
	mock.ExpectCommit()
	called := false
	require.NoError(t, repo.withSystemTx(context.Background(), func(tx *gorm.DB) error {
		called = tx != nil
		return nil
	}))
	require.True(t, called)
	require.NoError(t, mock.ExpectationsWereMet())
}
