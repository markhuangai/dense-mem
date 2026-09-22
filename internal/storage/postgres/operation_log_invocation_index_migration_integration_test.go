//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOperationLogInvocationIndexMigrationCreatesRecoverablePartialIndex(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	// Goose reserves one provider connection while the no-transaction Go migration
	// reserves another connection for its session-scoped DDL settings.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)

	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	assertOperationLogInvocationIndex(t, ctx, db)
	assertMigrationControlRetirementIsPending(t, ctx, db)
	var lockTimeout string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
	require.Equal(t, "0", lockTimeout)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	// A second application must leave a valid index and not create a duplicate.
	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	assertOperationLogInvocationIndex(t, ctx, db)

	require.NoError(t, runtimeMigrationDownTo(ctx, db, 20260919010002))
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_class AS index_class
			JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
			WHERE namespace.nspname = 'public'
			  AND index_class.relname = 'operation_logs_team_invocation_timestamp_idx'
		)
	`).Scan(&exists))
	require.False(t, exists)

	// A held table lock must bound the concurrent DDL, after which the same
	// migration can retry successfully once the lock is released.
	assertOperationLogMigrationLockTimeout(t, ctx, db)
	assertOperationLogInvocationIndexMigrationSessionReset(t, ctx, db)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	assertOperationLogInvocationIndex(t, ctx, db)
	require.NoError(t, runtimeMigrationDownTo(ctx, db, 20260919010002))

	// A valid index from a partially recorded attempt must survive the retry.
	require.NoError(t, createValidOperationLogInvocationIndex(ctx, db))
	beforeOID := operationLogInvocationIndexOID(t, ctx, db)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	require.Equal(t, beforeOID, operationLogInvocationIndexOID(t, ctx, db))
	require.NoError(t, runtimeMigrationDownTo(ctx, db, 20260919010002))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class WHERE relname = 'operation_logs_team_invocation_timestamp_idx'
		)
	`).Scan(&exists))
	require.False(t, exists)

	// A canceled concurrent build leaves an invalid catalog entry that the
	// migration must rename, remove, and replace on its next attempt.
	createCanceledRelationshipTelemetryIndex(t, ctx, db, "operation_logs_team_invocation_timestamp_idx", "operation_logs", "team_id, (attrs ->> 'invocation_id'), timestamp DESC, id DESC")
	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	assertOperationLogInvocationIndex(t, ctx, db)
}

func assertOperationLogInvocationIndexMigrationSessionReset(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	connections := make([]*sql.Conn, 0, 2)
	for range 2 {
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for _, conn := range connections {
		var lockTimeout, txMode string
		require.NoError(t, conn.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
		require.Equal(t, "0", lockTimeout)
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT current_setting('app.tx_mode', true)").Scan(&txMode))
		require.Empty(t, txMode)
	}
}

func runtimeMigrationUpTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newRuntimeMigrationProvider(getMigrationsDir(), db, false)
	if err != nil {
		return err
	}
	_, err = provider.UpTo(ctx, version)
	return err
}

func runtimeMigrationDownTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newRuntimeMigrationProvider(getMigrationsDir(), db, false)
	if err != nil {
		return err
	}
	_, err = provider.DownTo(ctx, version)
	return err
}

func assertOperationLogMigrationLockTimeout(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	blocker, err := db.Conn(ctx)
	require.NoError(t, err)
	defer blocker.Close()
	_, err = blocker.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	defer func() { _, _ = blocker.ExecContext(context.Background(), "ROLLBACK") }()
	_, err = blocker.ExecContext(ctx, "LOCK TABLE operation_logs IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion) }()
	var migrationErr error
	select {
	case migrationErr = <-done:
	case <-time.After(35 * time.Second):
		t.Fatal("operation-log index migration did not honor its lock timeout")
	}
	require.Error(t, migrationErr)
	require.Contains(t, strings.ToLower(migrationErr.Error()), "lock timeout")
}

func assertMigrationControlRetirementIsPending(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM goose_db_version
			WHERE version_id = 20260917010001 AND is_applied
		)
	`).Scan(&applied))
	require.False(t, applied)
}

func assertOperationLogInvocationIndex(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var valid bool
	var predicate string
	var definition string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT state.indisvalid, pg_get_expr(state.indpred, state.indrelid), pg_get_indexdef(state.indexrelid)
		FROM pg_index AS state
		JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
		JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND index_class.relname = 'operation_logs_team_invocation_timestamp_idx'
	`).Scan(&valid, &predicate, &definition))
	require.True(t, valid)
	lowerPredicate := strings.ToLower(predicate)
	require.Contains(t, lowerPredicate, "invocation_id")
	require.Contains(t, lowerPredicate, "is not null")
	require.Contains(t, strings.ToLower(definition), "team_id")
	require.Contains(t, strings.ToLower(definition), "invocation_id")
}

func createValidOperationLogInvocationIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE INDEX CONCURRENTLY operation_logs_team_invocation_timestamp_idx
		    ON operation_logs(team_id, (attrs ->> 'invocation_id'), timestamp DESC, id DESC)
		    WHERE (attrs ->> 'invocation_id') IS NOT NULL
	`)
	return err
}

func operationLogInvocationIndexOID(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var oid int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT index_class.oid::bigint
		FROM pg_class AS index_class
		JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND index_class.relname = 'operation_logs_team_invocation_timestamp_idx'
	`).Scan(&oid))
	return oid
}
