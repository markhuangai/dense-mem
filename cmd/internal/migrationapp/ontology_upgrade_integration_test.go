//go:build integration

package migrationapp

import (
	"context"
	"testing"
	"time"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunUpAutomaticallyRecoversStoppedOntology(t *testing.T) {
	for _, interrupted := range []string{"before_pause", "after_pause", "after_migration"} {
		t.Run(interrupted, func(t *testing.T) {
			db, admin := openOntologyUpgradeRuntimeDB(t)
			seedOntologyUpgrade(t, db)
			before := ontologyUpgradeSnapshot(t, db)
			ctx := context.Background()
			if interrupted != "before_pause" {
				service := ontology.NewMaintenanceUpgradeService(ontologypostgres.NewStore(db, storage.NewRLS()), auditapp.New(auditpostgres.NewStore(db)))
				require.NoError(t, service.Prepare(ctx, nil))
				var sourceStatus string
				require.NoError(t, admin.Raw(`SELECT status FROM ontology_maintenance_sources WHERE last_run_id=(SELECT run_id FROM ontology_maintenance_runs WHERE operation_key='ontology-upgrade-fixture')`).Row().Scan(&sourceStatus))
				require.Equal(t, "budget_deferred", sourceStatus)
			}
			if interrupted == "after_migration" {
				require.NoError(t, storage.RunUp(ctx, db))
			}
			require.NoError(t, RunUp(ctx, db, time.Minute, newRecordingLogger()))
			require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
			var paused bool
			var commands, running, retired int
			require.NoError(t, admin.Raw(`SELECT paused FROM ontology_maintenance_state`).Row().Scan(&paused))
			require.False(t, paused)
			require.NoError(t, admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE operation_key IN (?,?)`, contract.OntologyUpgradePauseKey, contract.OntologyUpgradeResumeKey).Row().Scan(&commands))
			require.Equal(t, 2, commands)
			require.NoError(t, admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'`).Row().Scan(&running))
			require.Zero(t, running)
			require.NoError(t, admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=? AND metadata->>'migration_version'=? AND metadata->>'outcome'='window_closed'`, contract.OntologyBudgetRetirementAuditOperation, "20261023020000").Row().Scan(&retired))
			require.Equal(t, 1, retired)
			require.NoError(t, RunUp(ctx, db, time.Minute, newRecordingLogger()))
			require.NoError(t, admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, contract.OntologyBudgetRetirementAuditOperation).Row().Scan(&retired))
			require.Equal(t, 1, retired)
			require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
		})
	}
}

func TestRunUpOntologyWaitAndConcurrentStarters(t *testing.T) {
	db, admin := openOntologyUpgradeRuntimeDB(t)
	batch := seedOntologyUpgrade(t, db)
	before := ontologyUpgradeSnapshot(t, db)
	ctx := context.Background()
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '1 hour' WHERE batch_id=?::uuid`, batch).Error
	}))
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := RunUp(waitCtx, db, 0, newRecordingLogger())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var applied bool
	require.NoError(t, db.Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)`, contract.OntologyBudgetRetirementVersion).Row().Scan(&applied))
	require.False(t, applied)
	require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
	var retired int
	require.NoError(t, admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, contract.OntologyBudgetRetirementAuditOperation).Row().Scan(&retired))
	require.Zero(t, retired)
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '2 seconds' WHERE batch_id=?::uuid`, batch).Error
	}))
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- RunUp(context.Background(), db, time.Minute, newRecordingLogger()) }()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
	require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
	require.NoError(t, admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, contract.OntologyBudgetRetirementAuditOperation).Row().Scan(&retired))
	require.Equal(t, 1, retired)
}
