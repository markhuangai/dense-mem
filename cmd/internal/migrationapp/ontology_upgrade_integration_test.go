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
)

func TestRunUpAutomaticallyRecoversStoppedOntology(t *testing.T) {
	for _, interrupted := range []string{"before_pause", "after_pause", "after_migration"} {
		t.Run(interrupted, func(t *testing.T) {
			db := openMigrationIntegrationDB(t)
			seedOntologyUpgrade(t, db)
			before := ontologyUpgradeSnapshot(t, db)
			ctx := context.Background()
			if interrupted != "before_pause" {
				service := ontology.NewMaintenanceUpgradeService(ontologypostgres.NewStore(db, storage.NewRLS()), auditapp.New(auditpostgres.NewStore(db)))
				require.NoError(t, service.Prepare(ctx, nil))
			}
			if interrupted == "after_migration" {
				require.NoError(t, storage.RunUp(ctx, db))
			}
			require.NoError(t, RunUp(ctx, db, time.Minute, newRecordingLogger()))
			require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
			var paused bool
			var commands, running int
			require.NoError(t, db.Raw(`SELECT paused FROM ontology_maintenance_state`).Row().Scan(&paused))
			require.False(t, paused)
			require.NoError(t, db.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE operation_key IN (?,?)`, contract.OntologyUpgradePauseKey, contract.OntologyUpgradeResumeKey).Row().Scan(&commands))
			require.Equal(t, 2, commands)
			require.NoError(t, db.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'`).Row().Scan(&running))
			require.Zero(t, running)
			require.NoError(t, RunUp(ctx, db, time.Minute, newRecordingLogger()))
			require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
		})
	}
}

func TestRunUpOntologyWaitAndConcurrentStarters(t *testing.T) {
	db := openMigrationIntegrationDB(t)
	batch := seedOntologyUpgrade(t, db)
	before := ontologyUpgradeSnapshot(t, db)
	require.NoError(t, db.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '1 hour' WHERE batch_id=?::uuid`, batch).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := RunUp(ctx, db, 0, newRecordingLogger())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var applied bool
	require.NoError(t, db.Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)`, contract.OntologyBudgetRetirementVersion).Row().Scan(&applied))
	require.False(t, applied)
	require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
	require.NoError(t, db.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '2 seconds' WHERE batch_id=?::uuid`, batch).Error)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- RunUp(context.Background(), db, time.Minute, newRecordingLogger()) }()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
	require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
}
