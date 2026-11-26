//go:build integration

package migrationapp

import (
	"context"
	"testing"
	"time"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
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
				service := topics.NewMaintenanceUpgradeService(topicspostgres.NewStore(db, storage.NewRLS()), auditapp.New(auditpostgres.NewStore(db)))
				require.NoError(t, service.Prepare(ctx, nil))
				var sourceStatus string
				require.NoError(t, admin.Raw(`SELECT status FROM ontology_maintenance_sources WHERE last_run_id=(SELECT run_id FROM ontology_maintenance_runs WHERE operation_key='ontology-upgrade-fixture')`).Row().Scan(&sourceStatus))
				require.Equal(t, "budget_deferred", sourceStatus)
			}
			if interrupted == "after_migration" {
				require.NoError(t, storage.RunUp(ctx, db))
			}
			migrator, err := storage.NewMigrator(db)
			require.NoError(t, err)
			if interrupted != "after_migration" {
				require.ErrorContains(t, RunUp(ctx, db, time.Minute, newRecordingLogger()), "through released v2.8.0")
			}
			require.NoError(t, migrateTopicStorage(ctx, db, migrator, newRecordingLogger()))
			require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
			var paused bool
			var commands, running, retired int
			require.NoError(t, admin.Raw(`SELECT paused FROM topic_maintenance_state`).Row().Scan(&paused))
			require.False(t, paused)
			require.NoError(t, admin.Raw(`SELECT count(*) FROM topic_maintenance_runs WHERE operation_key IN (?,?)`, contract.OntologyUpgradePauseKey, contract.OntologyUpgradeResumeKey).Row().Scan(&commands))
			require.Equal(t, 2, commands)
			require.NoError(t, admin.Raw(`SELECT count(*) FROM topic_maintenance_batches WHERE status='running'`).Row().Scan(&running))
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
	t.Run("below floor preserves the source", testBelowFloorTopicStartup)
	t.Run("current layout drains before relational cutover", testCurrentTopicWriterDrain)
}

func testCurrentTopicWriterDrain(t *testing.T) {
	ctx := t.Context()
	db, admin := openOntologyUpgradeRuntimeDB(t)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(ctx))
	seedProgressMigration(t, db, 1, 0)
	require.NoError(t, migrator.RunUpTo(ctx, 20261124010004))
	setLease := func(live bool) {
		t.Helper()
		require.NoError(t, storage.NewRLS().WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			if err := tx.Exec(`SET LOCAL app.tx_mode='migration'`).Error; err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO topic_maintenance_teams(team_id,shared_space_id,space_generation,lease_token,lease_until)
				SELECT team_id,id,generation,gen_random_uuid(),clock_timestamp()+CASE WHEN ? THEN interval '1 hour' ELSE interval '-1 second' END
				FROM memory_spaces WHERE kind='team_shared'
				ON CONFLICT(team_id,shared_space_id,space_generation) DO UPDATE
				SET lease_token=excluded.lease_token,lease_until=excluded.lease_until`, live).Error
		}))
	}
	setLease(true)
	pausedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	logs := newRecordingLogger()
	waiting := make(chan error, 1)
	go func() { waiting <- RunUp(pausedCtx, db, time.Minute, logs) }()
	require.Eventually(t, func() bool {
		for _, entry := range logs.entriesSnapshot() {
			if entry.message == "topic_storage_drain" && entry.attrs["active_preparations"] == int64(1) {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-waiting, context.Canceled)
	var applied bool
	require.NoError(t, admin.Raw(`SELECT to_regclass('public.topics') IS NOT NULL`).Row().Scan(&applied))
	require.False(t, applied, "startup must not install the fence while a current-layout writer is live")
	setLease(false)

	blocker := admin.WithContext(ctx).Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec(`LOCK TABLE operation_logs IN SHARE UPDATE EXCLUSIVE MODE`).Error)
	waitForPrepareRetry := func(logger *recordingLogger) {
		t.Helper()
		require.Eventually(t, func() bool {
			for _, entry := range logger.entriesSnapshot() {
				if entry.message == "relational_topic_migration_progress" && entry.attrs["phase"] == "prepare_lock" && entry.attrs["state"] == "waiting" {
					attempts, _ := entry.attrs["completed"].(*int64)
					if attempts != nil && *attempts >= 2 {
						return true
					}
				}
			}
			return false
		}, 8*time.Second, 20*time.Millisecond)
	}
	lockCtx, cancelLockWait := context.WithCancel(ctx)
	defer cancelLockWait()
	lockLogs := newRecordingLogger()
	go func() { waiting <- RunUp(lockCtx, db, 0, lockLogs) }()
	waitForPrepareRetry(lockLogs)
	cancelLockWait()
	require.ErrorIs(t, <-waiting, context.Canceled)
	require.NoError(t, admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='offline_upgrade_fence')`).Row().Scan(&applied))
	require.False(t, applied, "failed preparation attempts must roll back the write fence")

	timeoutLogs := newProgressLog(t)
	const timeoutReason = "feature_storage_relational_topics_prepare_lock_timeout: table lock unavailable (SQLSTATE 55P03)"
	err = RunUp(ctx, db, 6*time.Second, timeoutLogs.logger)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, timeoutReason, storage.MigrationFailureReason(err))
	var terminal map[string]any
	for _, event := range timeoutLogs.records(t) {
		if event["msg"] == "relational_topic_migration_failed" {
			require.Nil(t, terminal, "preparation must emit one terminal failure")
			terminal = event
		}
	}
	require.NotNil(t, terminal, "a held lock must produce a protected terminal record")
	require.Equal(t, "ERROR", terminal["level"])
	require.Equal(t, "prepare_lock", terminal["phase"])
	require.Equal(t, "failed", terminal["state"])
	require.Equal(t, timeoutReason, terminal["failure_reason"])
	require.Equal(t, "55P03", terminal["sqlstate"])
	require.Equal(t, float64(storage.RelationalTopicPrepareVersion), terminal["migration_version"])
	require.Equal(t, "attempts", terminal["metric"])
	require.Greater(t, terminal["completed"], float64(1))
	require.NotEmpty(t, terminal["migration_execution_id"])
	require.Equal(t, "resume_matching_application_after_resolving_reported_cause", terminal["recovery_action"])
	require.NoError(t, admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='offline_upgrade_fence')
		OR EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)`, storage.RelationalTopicPrepareVersion).Row().Scan(&applied))
	require.False(t, applied, "expired preparation must leave no write fence or applied version")

	results := make(chan error, 2)
	prepareLogs := newRecordingLogger()
	for _, timeout := range []time.Duration{0, 3 * time.Minute} {
		go func() { results <- RunUp(ctx, db, timeout, prepareLogs) }()
	}
	waitForPrepareRetry(prepareLogs)
	select {
	case err := <-results:
		t.Fatalf("migration returned while the preparation lock was held: %v", err)
	case <-time.After(65 * time.Second):
	}
	require.NoError(t, blocker.Rollback().Error)
	for range 2 {
		require.NoError(t, <-results)
	}
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, storage.ValidateStartupMigrationState(ctx, pool, storage.MigrationsDir()))
}

func testBelowFloorTopicStartup(t *testing.T) {
	db, admin := openOntologyUpgradeRuntimeDB(t)
	batch := seedOntologyUpgrade(t, db)
	before := ontologyUpgradeSnapshot(t, db)
	ctx := context.Background()
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '1 hour' WHERE batch_id=?::uuid`, batch).Error
	}))
	err := RunUp(ctx, db, time.Minute, newRecordingLogger())
	require.ErrorContains(t, err, "through released v2.8.0")
	var applied bool
	require.NoError(t, db.Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)`, contract.OntologyBudgetRetirementVersion).Row().Scan(&applied))
	require.False(t, applied)
	require.Equal(t, before, ontologyUpgradeSnapshot(t, db))
	var retired int
	require.NoError(t, admin.Raw(`SELECT count(*) FROM audit_log WHERE operation=?`, contract.OntologyBudgetRetirementAuditOperation).Row().Scan(&retired))
	require.Zero(t, retired)
}
