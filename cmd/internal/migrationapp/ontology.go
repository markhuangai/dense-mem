package migrationapp

import (
	"context"
	"fmt"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/topics"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
	"gorm.io/gorm"
)

func migrateOntology(ctx context.Context, db *gorm.DB, migrator *postgres.Migrator, logger infoLogger) error {
	service := ontology.NewMaintenanceUpgradeService(topicspostgres.NewStore(db, postgres.NewRLS()), audit.New(auditpostgres.NewStore(db)))
	required := false
	observe := func(phase string, state contract.MaintenanceUpgradeState) {
		required = required || state.Required
		logInfo(ctx, logger, "ontology upgrade preparation", "phase", phase,
			"migration_version", contract.OntologyBudgetRetirementVersion, "paused", state.Paused, "upgrade_owned_pause", state.OwnsPause,
			"live_batches", state.LiveBatches, "live_team_leases", state.LiveTeamLeases,
			"unreconciled_attempts", state.UnreconciledAttempts, "recovered_batches", state.RecoveredBatches, "closed_runs", state.ClosedRuns)
	}
	if err := service.Prepare(ctx, observe); err != nil {
		return fmt.Errorf("prepare ontology upgrade: %w", err)
	}
	if err := migrateEvidenceOnly(ctx, db, migrator, logger); err != nil {
		return err
	}
	if err := waitForTopicStorageDrain(ctx, db, logger); err != nil {
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
		return fmt.Errorf("restore ontology admission: %w", err)
	}
	observe("complete", state)
	return nil
}

// A stopped worker can leave a live lease behind. Its expiry releases capacity,
// but only the provider accounting owner can reconcile an admitted attempt.
func waitForTopicStorageDrain(ctx context.Context, db *gorm.DB, logger infoLogger) error {
	ctx, cancel := context.WithTimeout(ctx, ontology.MaintenanceUpgradeWaitLimit)
	defer cancel()
	var previous [3]int64
	for {
		var work [3]int64
		err := postgres.NewRLS().WithSystemReadOnlyRepeatableTx(ctx, db, func(tx *gorm.DB) error {
			var installed bool
			if err := tx.Raw(`SELECT to_regclass('public.ontology_maintenance_state') IS NOT NULL`).Row().Scan(&installed); err != nil || !installed {
				return err
			}
			return tx.Raw(`SELECT (SELECT count(*) FROM ontology_maintenance_batches WHERE status='running' AND lease_until>clock_timestamp()),
				(SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp()),
				(SELECT count(*) FROM ontology_maintenance_attempts WHERE reconciled_at IS NULL)`).Row().Scan(&work[0], &work[1], &work[2])
		})
		if err != nil {
			return fmt.Errorf("observe topic storage drain: %w", err)
		}
		if work != previous {
			logInfo(ctx, logger, "topic_storage_drain", "active_batches", work[0], "active_preparations", work[1], "unreconciled_attempts", work[2])
			previous = work
		}
		if work[0] == 0 && work[1] == 0 {
			if work[2] != 0 {
				return fmt.Errorf("topic storage needs terminal provider usage for %d attempts", work[2])
			}
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for topic storage drain: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
