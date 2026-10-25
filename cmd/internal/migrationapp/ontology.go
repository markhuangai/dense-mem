package migrationapp

import (
	"context"
	"fmt"

	audit "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

func migrateOntology(ctx context.Context, db *gorm.DB, migrator *postgres.Migrator, logger infoLogger) error {
	service := ontology.NewMaintenanceUpgradeService(ontologypostgres.NewStore(db, postgres.NewRLS()), audit.New(auditpostgres.NewStore(db)))
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
	if err := migrator.RunUp(ctx); err != nil {
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
