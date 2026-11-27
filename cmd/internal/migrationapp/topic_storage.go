package migrationapp

import (
	"context"
	"fmt"

	audit "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
	"gorm.io/gorm"
)

func migrateTopicStorage(ctx context.Context, db *gorm.DB, migrator *postgres.Migrator, logger infoLogger) error {
	service := topics.NewMaintenanceUpgradeService(topicspostgres.NewStore(db, postgres.NewRLS()), audit.New(auditpostgres.NewStore(db)))
	required := false
	observe := func(phase string, state contract.MaintenanceUpgradeState) {
		required = required || state.Required
		logInfo(ctx, logger, "topic storage upgrade preparation", "phase", phase,
			"migration_version", contract.OntologyBudgetRetirementVersion, "paused", state.Paused, "upgrade_owned_pause", state.OwnsPause,
			"live_batches", state.LiveBatches, "live_team_leases", state.LiveTeamLeases,
			"unreconciled_attempts", state.UnreconciledAttempts, "recovered_batches", state.RecoveredBatches, "closed_runs", state.ClosedRuns)
	}
	if err := service.Prepare(ctx, observe); err != nil {
		return fmt.Errorf("prepare topic storage upgrade: %w", err)
	}
	if err := migrateEvidenceOnly(ctx, db, migrator, logger); err != nil {
		return err
	}
	if err := migrateFeatureStorage(ctx, migrator, logger); err != nil {
		return err
	}
	if !required {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if err := postgres.ValidateStartupMigrationState(ctx, sqlDB, postgres.MigrationsDir()); err != nil {
		return err
	}
	state, err := service.Complete(ctx)
	if err != nil {
		return fmt.Errorf("restore Topic maintenance admission: %w", err)
	}
	observe("complete", state)
	return nil
}

func reportTopicMaintenanceCleanup(ctx context.Context, logger infoLogger, summary postgres.TopicMaintenanceCleanupSummary) {
	const event = "topic_maintenance_upgrade_cleanup"
	attrs := []any{
		"migration_execution_id", summary.ExecutionID,
		"relation_prefix", summary.RelationPrefix,
		"current_version", summary.CurrentVersion,
		"target_version", summary.TargetVersion,
		"elapsed_ms", summary.Elapsed.Milliseconds(),
		"transaction_outcome", summary.TransactionOutcome,
	}
	if summary.Err != nil {
		logError(context.WithoutCancel(ctx), logger, event+"_failed", append(attrs,
			"phase", summary.Phase,
			"failure_reason", postgres.MigrationFailureReason(summary.Err),
			"sqlstate", summary.SQLState,
			"recovery_action", "stop_active_writers_and_retry_upgrade",
		)...)
		return
	}
	if !summary.Executed {
		return
	}
	attrs = append(attrs,
		"discarded_batches", summary.DiscardedBatches,
		"cleared_team_leases", summary.ClearedTeamLeases,
		"cleared_community_leases", summary.ClearedCommunityLeases,
		"cleared_consolidation_leases", summary.ClearedConsolidationLeases,
		"reconciled_attempts", summary.ReconciledAttempts,
	)
	if summary.DiscardedTotal() > 0 {
		logWarn(ctx, logger, event, attrs...)
		return
	}
	logInfo(ctx, logger, event, attrs...)
}
