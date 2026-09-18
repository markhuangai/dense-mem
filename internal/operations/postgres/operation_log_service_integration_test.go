//go:build integration

package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/observability"
	operationsapp "github.com/markhuangai/dense-mem/internal/operations"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestOperationLogRootFanoutPersistsOneEventWithoutRepositoryRecursion(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()

	root := observability.NewWithHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: observability.LevelTrace}))
	sinkClient, err := storagepostgres.OpenOperationLogClient(ctx, operationLogDSNConfig{dsn: operationLoggerDSN(appDB)}, root)
	require.NoError(t, err)
	defer sinkClient.Close()

	repo := NewOperationLogRepository(sinkClient.GetDB(), rls)
	service := operationsapp.NewOperationLogService(repo, nil)
	require.NoError(t, root.AttachSink(service))

	marker := "root-operation-log-" + uuid.NewString()
	root.InfoContext(ctx, marker,
		observability.String("event_marker", marker),
		observability.String("caller_function", "forged.Function"),
	)
	require.NoError(t, service.Flush(ctx))

	var (
		matchingRows int64
		totalRows    int64
		caller       string
	)
	queryCtx := observability.WithSinkSuppressed(ctx)
	require.NoError(t, rls.WithSystemTx(queryCtx, sinkClient.GetDB(), func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM operation_logs WHERE message = ?`, marker).Scan(&matchingRows).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM operation_logs`).Scan(&totalRows).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT attrs ->> 'caller_function' FROM operation_logs WHERE message = ?`, marker).Scan(&caller).Error
	}))
	assert.EqualValues(t, 1, matchingRows)
	assert.EqualValues(t, 1, totalRows, "the sink's own SQL must not recursively fan out")
	assert.Contains(t, caller, "TestOperationLogRootFanoutPersistsOneEventWithoutRepositoryRecursion")
	assert.NotEqual(t, "forged.Function", caller)
}

func TestOperationLogServicePersistsQueueGapRecoveryInPostgres(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	service := operationsapp.NewOperationLogService(NewOperationLogRepository(appDB, rls), nil)
	marker := "queue-gap-operation-log-" + uuid.NewString()

	for {
		err := service.WriteLog(ctx, observability.LogRecord{Message: marker})
		if errors.Is(err, operationsapp.ErrOperationLogQueueFull) {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, service.Flush(ctx))

	var (
		queuedRows int64
		gapRows    int64
	)
	require.NoError(t, rls.WithSystemTx(observability.WithSinkSuppressed(ctx), appDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM operation_logs WHERE message = ?`, marker).Scan(&queuedRows).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM operation_logs
			WHERE message = 'operation log gap recovered'
			  AND attrs ->> 'event' = 'operation_log_gap_recovered'
			  AND attrs ->> 'dropped_events' = '1'
		`).Scan(&gapRows).Error
	}))
	assert.Greater(t, queuedRows, int64(0))
	assert.EqualValues(t, 1, gapRows)
}

func TestOperationLogServiceShutdownFlushPersistsPendingEventInPostgres(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	service := operationsapp.NewOperationLogService(NewOperationLogRepository(appDB, rls), nil)
	service.Start(ctx)
	marker := "shutdown-operation-log-" + uuid.NewString()
	require.NoError(t, service.WriteLog(ctx, observability.LogRecord{Message: marker, Contextual: true}))

	require.NoError(t, service.Shutdown(ctx))

	var (
		matchingRows int64
		gapRows      int64
	)
	require.NoError(t, rls.WithSystemTx(observability.WithSinkSuppressed(ctx), appDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM operation_logs WHERE message = ?`, marker).Scan(&matchingRows).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM operation_logs
			WHERE message = 'operation log gap recovered'
			  AND timestamp >= ?
		`, time.Now().UTC().Add(-time.Minute)).Scan(&gapRows).Error
	}))
	assert.EqualValues(t, 1, matchingRows)
	assert.Zero(t, gapRows)
}

func operationLoggerDSN(db *gorm.DB) string {
	return db.Dialector.(*gormpostgres.Dialector).Config.DSN
}
