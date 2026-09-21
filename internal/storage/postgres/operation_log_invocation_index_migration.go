package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pressly/goose/v3"
)

const (
	operationLogInvocationIndexMigrationVersion int64 = 20260921010001
	operationLogInvocationIndexMigrationSQLName       = "20260921010001_operation_logs_invocation_index.sql"
	operationLogInvocationIndexMigrationGoName        = "v2_6/20260921010001_operation_logs_invocation_index.go"
)

func init() {
	goose.AddNamedMigrationNoTxContext(
		operationLogInvocationIndexMigrationGoName,
		operationLogInvocationIndexMigrationUp,
		operationLogInvocationIndexMigrationDown,
	)
}

func operationLogInvocationIndexMigrationUp(ctx context.Context, db *sql.DB) error {
	return runOperationLogInvocationIndexMigration(ctx, db, true)
}

func operationLogInvocationIndexMigrationDown(ctx context.Context, db *sql.DB) error {
	return runOperationLogInvocationIndexMigration(ctx, db, false)
}

func runOperationLogInvocationIndexMigration(ctx context.Context, db *sql.DB, up bool) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, conn.Close())
	}()
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		for _, statement := range []string{"RESET app.tx_mode", "RESET lock_timeout"} {
			_, cleanupErr := conn.ExecContext(cleanupCtx, statement)
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	for _, statement := range []string{
		"SET app.tx_mode = 'migration'",
		"SET lock_timeout = '30s'",
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if up {
		for _, statement := range []string{
			"DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid",
			operationLogInvocationIndexRecoverySQL,
			"DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid",
			operationLogInvocationIndexCreateSQL,
			"DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid",
		} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}

	for _, statement := range []string{
		"DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx",
		"DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid",
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

const operationLogInvocationIndexCreateSQL = `CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_team_invocation_timestamp_idx
    ON operation_logs(
        team_id,
        (attrs ->> 'invocation_id'),
        timestamp DESC,
        id DESC
    )
    WHERE (attrs ->> 'invocation_id') IS NOT NULL`

const operationLogInvocationIndexRecoverySQL = `DO $dense_mem_operation_logs_invocation_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'operation_logs_team_invocation_timestamp_idx'
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_operation_logs_invocation_index_recovery$`
