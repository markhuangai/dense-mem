package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	gooselock "github.com/pressly/goose/v3/lock"
)

const (
	topicStoragePrepareVersion           int64 = 20261124010000
	topicMaintenanceCleanupFailurePrefix       = "topic_maintenance_upgrade_cleanup: "
)

// TopicMaintenanceCleanupSummary describes one transaction during an offline schema upgrade.
type TopicMaintenanceCleanupSummary struct {
	Executed                   bool
	ExecutionID                string
	RelationPrefix             string
	CurrentVersion             int64
	TargetVersion              int64
	DiscardedBatches           int64
	ClearedTeamLeases          int64
	ClearedCommunityLeases     int64
	ClearedConsolidationLeases int64
	ReconciledAttempts         int64
	Elapsed                    time.Duration
	Phase                      string
	TransactionOutcome         string
	SQLState                   string
	Err                        error
}

func (s TopicMaintenanceCleanupSummary) DiscardedTotal() int64 {
	return s.DiscardedBatches + s.ClearedTeamLeases + s.ClearedCommunityLeases + s.ClearedConsolidationLeases + s.ReconciledAttempts
}

type topicMaintenanceCleanupObserverKey struct{}

func WithTopicMaintenanceCleanupObserver(ctx context.Context, observe func(TopicMaintenanceCleanupSummary)) context.Context {
	return context.WithValue(ctx, topicMaintenanceCleanupObserverKey{}, observe)
}

func (m *Migrator) cleanupInflightTopicMaintenance(ctx context.Context, targetVersion int64) (err error) {
	if targetVersion < topicStoragePrepareVersion {
		return nil
	}
	started := time.Now()
	summary := TopicMaintenanceCleanupSummary{TargetVersion: targetVersion, Phase: "cleanup_lock", TransactionOutcome: "not_started"}
	if progress := MigrationProgressFromContext(ctx); progress != nil {
		summary.ExecutionID = progress.Snapshot().ExecutionID
	}
	defer func() {
		if err != nil {
			summary.SQLState, summary.Err = topicMaintenanceCleanupFailure(err)
			err = summary.Err
		}
		if observe, _ := ctx.Value(topicMaintenanceCleanupObserverKey{}).(func(TopicMaintenanceCleanupSummary)); observe != nil && (summary.Executed || summary.Err != nil) {
			summary.Elapsed = time.Since(started)
			observe(summary)
		}
	}()

	locker, err := gooselock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := (observedMigrationLocker{locker}).SessionLock(ctx, conn); err != nil {
		return errors.Join(err, DiscardAdvisoryLockConnection(conn))
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if unlockErr := locker.SessionUnlock(unlockCtx, conn); unlockErr != nil {
			if err == nil {
				summary.Phase = "cleanup_lock"
			}
			err = errors.Join(err, unlockErr, DiscardAdvisoryLockConnection(conn))
		}
	}()

	summary.Phase = "cleanup_tx"
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			summary.TransactionOutcome = "unconfirmed"
			err = errors.Join(err, rollbackErr)
		}
	}()
	summary.TransactionOutcome = "rolled_back"
	if _, err := tx.ExecContext(ctx, "SET LOCAL app.tx_mode = 'migration'"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return err
	}

	// Check the version after taking the same lock as Goose so a delayed
	// starter cannot discard maintenance admitted after the upgrade.
	var hasHistory bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass('public.goose_db_version') IS NOT NULL").Scan(&hasHistory); err != nil || !hasHistory {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied").Scan(&summary.CurrentVersion); err != nil {
		return err
	}
	if summary.CurrentVersion < evidenceOnlyMigrationVersion || summary.CurrentVersion >= summary.TargetVersion {
		return nil
	}
	if err := cleanupTopicMaintenanceRows(ctx, tx, &summary); err != nil || summary.RelationPrefix == "" {
		return err
	}
	summary.TransactionOutcome = "unconfirmed"
	if err := tx.Commit(); err != nil {
		return err
	}
	summary.Executed = true
	summary.TransactionOutcome = "committed"
	return nil
}

func cleanupTopicMaintenanceRows(ctx context.Context, tx *sql.Tx, summary *TopicMaintenanceCleanupSummary) error {
	var topic, ontology, communityLeases, consolidationLeases bool
	err := tx.QueryRowContext(ctx, `
		SELECT to_regclass('public.topic_maintenance_state') IS NOT NULL,
		       to_regclass('public.ontology_maintenance_state') IS NOT NULL,
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema = 'public' AND table_name = 'topic_maintenance_community_work'
		                 AND column_name = 'lease_batch_id'),
		       to_regclass('public.topic_embeddings') IS NOT NULL
	`).Scan(&topic, &ontology, &communityLeases, &consolidationLeases)
	if err != nil {
		return err
	}
	switch {
	case topic:
		summary.RelationPrefix = "topic"
	case ontology:
		summary.RelationPrefix = "ontology"
	default:
		return nil
	}

	type cleanupUpdate struct {
		query string
		count *int64
	}
	updates := []cleanupUpdate{
		{fmt.Sprintf(`UPDATE %[1]s_maintenance_batches SET status = 'lost',
			failure_code = CASE WHEN failure_code = '' THEN 'upgrade_interrupted' ELSE failure_code END,
			completed_at = COALESCE(completed_at, clock_timestamp()) WHERE status = 'running'`, summary.RelationPrefix), &summary.DiscardedBatches},
		{fmt.Sprintf(`UPDATE %[1]s_maintenance_teams SET lease_token = NULL, lease_until = NULL
			WHERE lease_token IS NOT NULL OR lease_until IS NOT NULL`, summary.RelationPrefix), &summary.ClearedTeamLeases},
	}
	if communityLeases {
		updates = append(updates, cleanupUpdate{`UPDATE topic_maintenance_community_work SET lease_batch_id = NULL, lease_until = NULL
			WHERE lease_batch_id IS NOT NULL OR lease_until IS NOT NULL`, &summary.ClearedCommunityLeases})
	}
	if consolidationLeases {
		updates = append(updates, cleanupUpdate{`UPDATE topic_embeddings SET consolidation_batch_id = NULL, consolidation_lease_until = NULL
			WHERE consolidation_batch_id IS NOT NULL OR consolidation_lease_until IS NOT NULL`, &summary.ClearedConsolidationLeases})
	}
	updates = append(updates, cleanupUpdate{fmt.Sprintf(`UPDATE %[1]s_maintenance_attempts SET reported_input = COALESCE(reported_input, 0),
		reported_output = COALESCE(reported_output, 0), reported_total = COALESCE(reported_total, 0),
		reconciled_at = clock_timestamp() WHERE reconciled_at IS NULL`, summary.RelationPrefix), &summary.ReconciledAttempts})
	for _, update := range updates {
		result, err := tx.ExecContext(ctx, update.query)
		if err != nil {
			return err
		}
		*update.count, err = result.RowsAffected()
		if err != nil {
			return err
		}
	}
	return nil
}

func topicMaintenanceCleanupFailure(cause error) (string, *MigrationFailure) {
	reason := MigrationFailureReason(cause)
	var state string
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) && validSQLState(pgErr.Code) {
		state = pgErr.Code
		if state == "55P03" {
			reason = "table lock unavailable (SQLSTATE 55P03)"
		}
	}
	return state, &MigrationFailure{Cause: cause, Reason: topicMaintenanceCleanupFailurePrefix + reason}
}
