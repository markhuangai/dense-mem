package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
)

const (
	topicMaintenanceIndexesVersion int64 = 20261128010000
	topicMaintenanceIndexesFile          = "20261128010000_topic_maintenance_indexes.sql"
	topicMaintenanceIndexesGoName        = "v2_8/20261128010000_topic_maintenance_indexes.go"
)

func init() {
	goose.AddNamedMigrationNoTxContext(topicMaintenanceIndexesGoName, topicMaintenanceIndexesUp, topicMaintenanceIndexesDown)
}

func topicMaintenanceIndexesDown(context.Context, *sql.DB) error {
	return errors.New("topic maintenance index migration requires forward recovery")
}

func topicMaintenanceIndexesUp(ctx context.Context, db *sql.DB) (resultErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, conn.Close()) }()
	if err := addTeamMaintenanceProgressColumn(ctx, conn); err != nil {
		return fmt.Errorf("topic maintenance teams progress column: %w", err)
	}
	var previousLockTimeout string
	if err := conn.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&previousLockTimeout); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, cleanupErr := conn.ExecContext(cleanupCtx, `SELECT set_config('lock_timeout',$1,false)`, previousLockTimeout)
		if cleanupErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	if _, err := conn.ExecContext(ctx, "SET lock_timeout='30s'"); err != nil {
		return err
	}
	indexes := []struct{ name, ddl string }{
		{
			"topic_maintenance_attempts_unreconciled_idx",
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_attempts_unreconciled_idx ON topic_maintenance_attempts(batch_id) WHERE reconciled_at IS NULL`,
		},
		{
			"topic_maintenance_attempts_window_idx",
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_attempts_window_idx ON topic_maintenance_attempts(window_id)`,
		},
		{
			"topic_maintenance_sources_failed_run_idx",
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_sources_failed_run_idx ON topic_maintenance_sources(last_run_id) WHERE eligible AND status='failed'`,
		},
		{
			"topic_maintenance_sources_equiv_failed_run_idx",
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_sources_equiv_failed_run_idx ON topic_maintenance_sources(equivalence_last_run_id) WHERE eligible AND equivalence_status='failed'`,
		},
		{
			"topic_maintenance_batches_running_run_idx",
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_batches_running_run_idx ON topic_maintenance_batches(run_id) WHERE status='running'`,
		},
	}
	for _, index := range indexes {
		var valid bool
		err := conn.QueryRowContext(ctx, `SELECT state.indisvalid FROM pg_index AS state
			JOIN pg_class AS candidate ON candidate.oid=state.indexrelid
			JOIN pg_namespace AS namespace ON namespace.oid=candidate.relnamespace
			WHERE namespace.nspname='public' AND candidate.relname=$1`, index.name).Scan(&valid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !valid {
			if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS public."+index.name); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, index.ddl); err != nil {
			return fmt.Errorf("topic maintenance index %s: %w", index.name, err)
		}
	}
	return nil
}

func addTeamMaintenanceProgressColumn(ctx context.Context, conn *sql.Conn) (resultErr error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE topic_maintenance_teams ADD COLUMN IF NOT EXISTS last_successful_progress TIMESTAMPTZ NULL`); err != nil {
		return err
	}
	return tx.Commit()
}
