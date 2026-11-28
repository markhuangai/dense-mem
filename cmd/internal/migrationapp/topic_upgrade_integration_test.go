//go:build integration

package migrationapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	topicUpgradeV281           int64 = 20261114010000
	topicUpgradeV290           int64 = 20261124010004
	topicUpgradeV291RC2        int64 = 20261126010001
	topicUpgradeCleanupVersion int64 = 20261127010000
	topicUpgradeTargetVersion  int64 = 20261128010000
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
	t.Run("fresh startup skips cleanup", func(t *testing.T) {
		db, _ := openOntologyUpgradeRuntimeDB(t)
		logs := newProgressLog(t)
		require.NoError(t, RunUp(t.Context(), db, time.Minute, logs.logger))
		require.Empty(t, topicMaintenanceCleanupEvents(t, logs))
		require.True(t, progressHas(t, logs, "postgres migrations completed", ""))
	})
	t.Run("dirty upgrades discard in-flight work", testTopicMaintenanceUpgradeCleanup)
	t.Run("cleanup lock timeout leaves the source intact", testTopicMaintenanceCleanupRollback)
	t.Run("a delayed starter and ordinary restart preserve post-upgrade work", testTopicMaintenanceCleanupConcurrentStarters)
	t.Run("only a pending migration cleans dirty state", testTopicMaintenanceCleanupForwardMigration)
	t.Run("relational preparation retries lock contention", testRelationalTopicPrepareLockRetry)
	t.Run("preparation deadline during version read preserves the committed fence", testRelationalTopicPrepareDeadlineAfterCommit)
}

func testTopicMaintenanceUpgradeCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		version      int64
		stageLeases  int64
	}{
		{"v2.8.1", "ontology", topicUpgradeV281, 0},
		{"feature copy fenced", "ontology", storage.FeatureStoragePrepareVersion, 0},
		{"v2.9.0", "topic", topicUpgradeV290, 0},
		{"relational copy fenced", "topic", storage.RelationalTopicPrepareVersion, 0},
		{"v2.9.1-rc.2", "topic", topicUpgradeV291RC2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			db, admin := openTopicMaintenanceCleanupFixture(t, tc.version)
			window := seedInterruptedTopicMaintenance(t, db, tc.version)
			evidence := progressEvidenceFingerprint(t, db)
			var revisions string
			revisionQuery := `SELECT jsonb_agg(jsonb_build_array(record_id, version, fingerprint) ORDER BY record_id, version)::text
				FROM %s_record_revisions WHERE kind = 'topic'`
			require.NoError(t, admin.Raw(fmt.Sprintf(revisionQuery, tc.prefix)).Row().Scan(&revisions))
			logs := newProgressLog(t)
			require.NoError(t, RunUp(ctx, db, time.Minute, logs.logger))

			events := topicMaintenanceCleanupEvents(t, logs)
			require.Len(t, events, 1)
			event := events[0]
			require.Equal(t, "topic_maintenance_upgrade_cleanup", event["msg"])
			require.Equal(t, "WARN", event["level"])
			require.NotEmpty(t, event["migration_execution_id"])
			require.Equal(t, tc.prefix, event["relation_prefix"])
			require.Equal(t, float64(tc.version), event["current_version"])
			require.Equal(t, float64(topicUpgradeTargetVersion), event["target_version"])
			require.Equal(t, "committed", event["transaction_outcome"])
			require.Equal(t, float64(2), event["discarded_batches"])
			require.Equal(t, float64(1), event["cleared_team_leases"])
			require.Equal(t, float64(tc.stageLeases), event["cleared_community_leases"])
			require.Equal(t, float64(tc.stageLeases), event["cleared_consolidation_leases"])
			require.Equal(t, float64(3), event["reconciled_attempts"])
			require.True(t, progressHas(t, logs, "postgres migrations completed", ""))

			assertCompletedTopicMaintenanceCleanup(t, db, admin)
			require.Equal(t, evidence, progressEvidenceFingerprint(t, db))
			var migratedRevisions string
			require.NoError(t, admin.Raw(fmt.Sprintf(revisionQuery, "topic")).Row().Scan(&migratedRevisions))
			require.Equal(t, revisions, migratedRevisions)
			if tc.stageLeases > 0 {
				resumeTopicMaintenanceAfterUpgrade(t, db, window)
			}
		})
	}
}

func openTopicMaintenanceCleanupFixture(t *testing.T, version int64) (*gorm.DB, *gorm.DB) {
	t.Helper()
	db, admin := openOntologyUpgradeRuntimeDB(t)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(t.Context()))
	seedProgressMigration(t, db, 2, 0)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(t.Context()))
	seedFeatureStorageProgress(t, db, 2)
	require.NoError(t, migrator.RunUpTo(t.Context(), version))
	return db, admin
}

func seedInterruptedTopicMaintenance(t *testing.T, db *gorm.DB, version int64) string {
	t.Helper()
	ctx := t.Context()
	prefix := "ontology"
	if version >= topicUpgradeV290 {
		prefix = "topic"
	}
	window, run, live := uuid.NewString(), uuid.NewString(), uuid.NewString()
	expired, lost := uuid.NewString(), uuid.NewString()
	hasStageLeases := version >= topicUpgradeV291RC2
	var embeddingContract string
	if hasStageLeases {
		active, err := searchpostgres.NewStore(db, storage.NewRLS()).EnsureActiveSearchContract(ctx,
			searchmaintenance.EnsureActiveSearchContractInput{Model: "topic-cleanup-fixture", Dimensions: 3})
		require.NoError(t, err)
		embeddingContract = active.Contract.EmbeddingContractID
	}
	require.NoError(t, storage.NewRLS().WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var team, space string
		var generation int64
		if err := tx.Raw(`SELECT spaces.team_id::text,spaces.id::text,spaces.generation
			FROM memory_spaces AS spaces JOIN teams ON teams.id=spaces.team_id
			WHERE spaces.kind='team_shared' AND teams.name='progress-fixture'`).Row().Scan(&team, &space, &generation); err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL app.tx_mode = 'migration'").Error; err != nil {
			return err
		}
		exec := func(query string, args ...any) error { return tx.Exec(fmt.Sprintf(query, prefix), args...).Error }
		if err := exec(`INSERT INTO %[1]s_maintenance_windows(window_id,starts_at,ends_at,policy)
			VALUES (?::uuid,clock_timestamp()-interval '1 hour',clock_timestamp()+interval '1 hour',
				'{"max_concurrency":1,"team_max_concurrency":1,"context_window_tokens":8192}')`, window); err != nil {
			return err
		}
		if err := exec(`INSERT INTO %[1]s_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
			VALUES (?::uuid,?::uuid,?,'topic-upgrade-fixture','{}','run','running',0,now(),now())`, run, window, "topic-upgrade-fixture:"+run); err != nil {
			return err
		}
		if err := exec(`INSERT INTO %[1]s_maintenance_teams(team_id,shared_space_id,space_generation,lease_token,lease_until)
			VALUES (?::uuid,?::uuid,?,gen_random_uuid(),now()+interval '1 hour')
			ON CONFLICT (team_id,shared_space_id,space_generation) DO UPDATE
			SET lease_token=excluded.lease_token,lease_until=excluded.lease_until`, team, space, generation); err != nil {
			return err
		}
		if err := exec(`INSERT INTO %[1]s_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,
			lease_token,lease_until,sources,revisions,status,failure_code,created_at,completed_at)
			SELECT batch.id,?::uuid,?::uuid,?::uuid,?::uuid,?,
				gen_random_uuid(),batch.lease_until,'[]','{}',batch.status,batch.failure,now(),batch.completed
			FROM (VALUES
				(?::uuid,now()+interval '1 hour','running','',NULL::timestamptz),
				(?::uuid,now()-interval '1 second','running','fixture_failure',now()),
				(?::uuid,now()-interval '1 second','lost','lease_lost',now())
			) AS batch(id,lease_until,status,failure,completed)`, run, window, team, space, generation, live, expired, lost); err != nil {
			return err
		}
		if err := exec(`INSERT INTO %[1]s_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,
			estimated_input_tokens,max_output_tokens,admitted_at)
			SELECT batch_id,gen_random_uuid(),1,window_id,0,0,100,40,now()
			FROM %[1]s_maintenance_batches WHERE run_id=?::uuid
			UNION ALL SELECT ?::uuid,gen_random_uuid(),2,?::uuid,0,0,100,40,now()`, run, lost, window); err != nil {
			return err
		}
		if err := exec(`UPDATE %[1]s_maintenance_attempts SET reported_input=11,reported_output=7,reported_total=18,
			reconciled_at=now() WHERE batch_id=?::uuid AND attempt=2`, lost); err != nil || !hasStageLeases {
			return err
		}
		if err := tx.Exec(`INSERT INTO topic_maintenance_community_work(team_id,shared_space_id,space_generation,topic_id,topic_version,ready_at,lease_batch_id,lease_until)
			SELECT team_id,shared_space_id,space_generation,topic_id,definition_version,now(),?::uuid,now()+interval '1 hour'
			FROM topics ORDER BY topic_id LIMIT 1
			ON CONFLICT (team_id,shared_space_id,space_generation,topic_id) DO UPDATE
			SET lease_batch_id=excluded.lease_batch_id,lease_until=excluded.lease_until`, live).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO topic_embeddings(team_id,shared_space_id,space_generation,topic_id,topic_version,embedding_contract_id,
			embedding_dimensions,embedding,consolidation_batch_id,consolidation_lease_until)
			SELECT team_id,shared_space_id,space_generation,topic_id,definition_version,?::uuid,3,'[1,0,0]'::vector,?::uuid,now()+interval '1 hour'
			FROM topics ORDER BY topic_id LIMIT 1`, embeddingContract, live).Error
	}))
	return window
}

func assertCompletedTopicMaintenanceCleanup(t *testing.T, db, admin *gorm.DB) {
	t.Helper()
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, storage.ValidateStartupMigrationState(t.Context(), pool, storage.MigrationsDir()))
	status, err := topicspostgres.NewStore(db, storage.NewRLS()).MaintenanceStatus(t.Context(), time.Now())
	require.NoError(t, err)
	require.Zero(t, status.ActiveWork)
	require.EqualValues(t, 4, status.Usage.Attempts)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.EqualValues(t, 3, status.Usage.UnavailableUsageAttempts)
	require.EqualValues(t, 11, status.Usage.ReportedInputTokens)
	require.EqualValues(t, 7, status.Usage.ReportedOutputTokens)
	require.EqualValues(t, 18, status.Usage.ReportedTotalTokens)

	var lost, interrupted, preserved, teamLeases, communityLeases, consolidationLeases int64
	require.NoError(t, admin.Raw(`SELECT
		count(*) FILTER (WHERE status='lost' AND completed_at IS NOT NULL),
		count(*) FILTER (WHERE failure_code='upgrade_interrupted' AND completed_at>=created_at),
		count(*) FILTER (WHERE failure_code IN ('fixture_failure','lease_lost') AND completed_at=created_at),
		(SELECT count(*) FROM topic_maintenance_teams WHERE lease_token IS NOT NULL OR lease_until IS NOT NULL),
		(SELECT count(*) FROM topic_maintenance_community_work WHERE lease_batch_id IS NOT NULL OR lease_until IS NOT NULL),
		(SELECT count(*) FROM topic_embeddings WHERE consolidation_batch_id IS NOT NULL OR consolidation_lease_until IS NOT NULL)
		FROM topic_maintenance_batches`).Row().Scan(&lost, &interrupted, &preserved, &teamLeases, &communityLeases, &consolidationLeases))
	require.EqualValues(t, 3, lost)
	require.EqualValues(t, 1, interrupted)
	require.EqualValues(t, 2, preserved)
	require.Zero(t, teamLeases)
	require.Zero(t, communityLeases)
	require.Zero(t, consolidationLeases)
}

func resumeTopicMaintenanceAfterUpgrade(t *testing.T, db *gorm.DB, window string) {
	t.Helper()
	ctx := t.Context()
	rls := storage.NewRLS()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO app_config(key,value) VALUES(?,'true') ON CONFLICT (key) DO UPDATE SET value=excluded.value`,
			domain.AppConfigTopicMaintenanceEnabled).Error
	}))
	store := topicspostgres.NewStore(db, rls)
	turn, err := store.ClaimMaintenanceTurn(ctx, window, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn, "the in-flight work must release capacity for a new maintenance stage")
	require.NoError(t, store.ReleaseMaintenanceTurn(ctx, *turn))
}

func topicMaintenanceCleanupEvents(t *testing.T, logs *progressLog) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, event := range logs.records(t) {
		if msg, _ := event["msg"].(string); strings.HasPrefix(msg, "topic_maintenance_upgrade_cleanup") {
			events = append(events, event)
		}
	}
	return events
}

func topicMaintenanceFixtureSnapshot(t *testing.T, admin *gorm.DB, withStageLeases bool) string {
	t.Helper()
	tables := []string{"topic_maintenance_batches", "topic_maintenance_teams", "topic_maintenance_attempts"}
	if withStageLeases {
		tables = append(tables, "topic_maintenance_community_work", "topic_embeddings")
	}
	var snapshot strings.Builder
	for _, table := range tables {
		var rows string
		require.NoError(t, admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text)::text,'[]') FROM `+table+` r`).Row().Scan(&rows))
		snapshot.WriteString(rows)
	}
	return snapshot.String()
}

func testTopicMaintenanceCleanupRollback(t *testing.T) {
	db, admin := openTopicMaintenanceCleanupFixture(t, topicUpgradeV290)
	seedInterruptedTopicMaintenance(t, db, topicUpgradeV290)
	before := topicMaintenanceFixtureSnapshot(t, admin, false)
	for _, table := range []string{"topic_maintenance_batches", "topic_maintenance_attempts"} {
		t.Run(table, func(t *testing.T) {
			blocker := admin.WithContext(t.Context()).Begin()
			require.NoError(t, blocker.Error)
			t.Cleanup(func() { _ = blocker.Rollback().Error })
			require.NoError(t, blocker.Exec("LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE").Error)

			logs := newProgressLog(t)
			err := RunUp(t.Context(), db, 15*time.Second, logs.logger)
			require.True(t, storage.IsLockNotAvailable(err), "%v", err)
			events := topicMaintenanceCleanupEvents(t, logs)
			require.Len(t, events, 1)
			event := events[0]
			require.Equal(t, "topic_maintenance_upgrade_cleanup_failed", event["msg"])
			require.Equal(t, "ERROR", event["level"])
			require.NotEmpty(t, event["migration_execution_id"])
			require.Equal(t, "topic", event["relation_prefix"])
			require.Equal(t, float64(topicUpgradeV290), event["current_version"])
			require.Equal(t, float64(topicUpgradeTargetVersion), event["target_version"])
			require.Equal(t, "cleanup_tx", event["phase"])
			require.Equal(t, "55P03", event["sqlstate"])
			require.Equal(t, "topic_maintenance_upgrade_cleanup: table lock unavailable (SQLSTATE 55P03)", event["failure_reason"])
			require.Equal(t, "rolled_back", event["transaction_outcome"])
			require.Equal(t, "stop_active_writers_and_retry_upgrade", event["recovery_action"])
			require.False(t, progressHas(t, logs, "postgres migrations completed", ""))
			require.NoError(t, blocker.Rollback().Error)
			require.Equal(t, before, topicMaintenanceFixtureSnapshot(t, admin, false))
		})
	}
	require.NoError(t, RunUp(t.Context(), db, time.Minute, newProgressLog(t).logger))
	assertCompletedTopicMaintenanceCleanup(t, db, admin)
}

func testTopicMaintenanceCleanupConcurrentStarters(t *testing.T) {
	ctx := t.Context()
	db, admin := openTopicMaintenanceCleanupFixture(t, topicUpgradeV291RC2)
	barrier := &gooseQueryBarrier{query: "SELECT pg_try_advisory_lock($1)", started: make(chan struct{}), proceed: make(chan struct{})}
	delayed := openTracedTopicStarter(t, db, barrier)
	delayedLogs := newProgressLog(t)
	result := make(chan error, 1)
	go func() { result <- RunUp(ctx, delayed, time.Minute, delayedLogs.logger) }()
	select {
	case <-barrier.started:
	case err := <-result:
		t.Fatalf("delayed starter returned before acquiring the Goose lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("delayed starter did not reach the Goose lock")
	}

	winnerLogs := newProgressLog(t)
	require.NoError(t, RunUp(ctx, db, time.Minute, winnerLogs.logger))
	clean := topicMaintenanceCleanupEvents(t, winnerLogs)
	require.Len(t, clean, 1)
	require.Equal(t, "INFO", clean[0]["level"])
	require.Equal(t, "committed", clean[0]["transaction_outcome"])
	for _, field := range []string{"discarded_batches", "cleared_team_leases", "cleared_community_leases", "cleared_consolidation_leases", "reconciled_attempts"} {
		require.Equal(t, float64(0), clean[0][field], field)
	}
	seedInterruptedTopicMaintenance(t, db, topicUpgradeCleanupVersion)
	live := topicMaintenanceFixtureSnapshot(t, admin, true)
	close(barrier.proceed)
	require.NoError(t, <-result)
	require.Empty(t, topicMaintenanceCleanupEvents(t, delayedLogs))
	require.Equal(t, live, topicMaintenanceFixtureSnapshot(t, admin, true))

	restartLogs := newProgressLog(t)
	require.NoError(t, RunUp(ctx, db, time.Minute, restartLogs.logger))
	require.Empty(t, topicMaintenanceCleanupEvents(t, restartLogs))
	require.Equal(t, live, topicMaintenanceFixtureSnapshot(t, admin, true))
}

func testTopicMaintenanceCleanupForwardMigration(t *testing.T) {
	db, admin := openTopicMaintenanceCleanupFixture(t, topicUpgradeV291RC2)
	seedInterruptedTopicMaintenance(t, db, topicUpgradeV291RC2)
	before := topicMaintenanceFixtureSnapshot(t, admin, true)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUpTo(t.Context(), topicUpgradeV291RC2+1))
	require.Equal(t, before, topicMaintenanceFixtureSnapshot(t, admin, true))

	pool, err := db.DB()
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, os.DirFS(filepath.Join(storage.MigrationsDir(), "v2_8")),
		goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	_, err = provider.ApplyVersion(t.Context(), topicUpgradeCleanupVersion, true)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(t.Context()))
	assertCompletedTopicMaintenanceCleanup(t, db, admin)
}

func testRelationalTopicPrepareLockRetry(t *testing.T) {
	ctx := t.Context()
	db, admin := openRelationalTopicPrepareFixture(t)
	var applied bool
	waiting := make(chan error, 1)

	blocker := lockTopicPreparation(t, admin)
	lockCtx, cancelLockWait := context.WithCancel(ctx)
	defer cancelLockWait()
	lockLogs := newRecordingLogger()
	go func() { waiting <- RunUp(lockCtx, db, 0, lockLogs) }()
	waitForPrepareRetry(t, lockLogs)
	cancelLockWait()
	require.ErrorIs(t, <-waiting, context.Canceled)
	require.NoError(t, admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='offline_upgrade_fence')`).Row().Scan(&applied))
	require.False(t, applied, "failed preparation attempts must roll back the write fence")

	timeoutLogs := newProgressLog(t)
	const timeoutReason = "feature_storage_relational_topics_prepare_lock_timeout: table lock unavailable (SQLSTATE 55P03)"
	err := RunUp(ctx, db, 6*time.Second, timeoutLogs.logger)
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
	for _, timeout := range []time.Duration{0, 3 * time.Minute} {
		prepareLogs := newRecordingLogger()
		go func() { results <- RunUp(ctx, db, timeout, prepareLogs) }()
		waitForPrepareRetry(t, prepareLogs)
	}
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

func openRelationalTopicPrepareFixture(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	ctx := t.Context()
	db, admin := openOntologyUpgradeRuntimeDB(t)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(ctx))
	seedProgressMigration(t, db, 1, 0)
	require.NoError(t, migrator.RunUpTo(ctx, 20261124010004))
	return db, admin
}

func lockTopicPreparation(t *testing.T, admin *gorm.DB) *gorm.DB {
	t.Helper()
	blocker := admin.WithContext(t.Context()).Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec(`LOCK TABLE operation_logs IN SHARE UPDATE EXCLUSIVE MODE`).Error)
	return blocker
}

func waitForPrepareRetry(t *testing.T, logger *recordingLogger) {
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

type gooseQueryBarrier struct {
	query   string
	once    sync.Once
	started chan struct{}
	proceed chan struct{}
}

func (barrier *gooseQueryBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if query.SQL == barrier.query {
		barrier.once.Do(func() {
			close(barrier.started)
			select {
			case <-barrier.proceed:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}

func (*gooseQueryBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func openTracedTopicStarter(t *testing.T, db *gorm.DB, barrier *gooseQueryBarrier) *gorm.DB {
	t.Helper()
	config, err := pgx.ParseConfig(db.Dialector.(*gormpostgres.Dialector).DSN)
	require.NoError(t, err)
	config.Tracer = barrier
	pool := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	traced, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: pool}), &gorm.Config{})
	require.NoError(t, err)
	return traced
}

func testRelationalTopicPrepareDeadlineAfterCommit(t *testing.T) {
	ctx := t.Context()
	db, admin := openRelationalTopicPrepareFixture(t)
	prepareBlocker := lockTopicPreparation(t, admin)
	barrier := &gooseQueryBarrier{query: "SELECT max(version_id) FROM goose_db_version", started: make(chan struct{}), proceed: make(chan struct{})}
	traced := openTracedTopicStarter(t, db, barrier)

	result := make(chan error, 1)
	logs := newRecordingLogger()
	go func() { result <- RunUp(ctx, traced, 10*time.Second, logs) }()
	waitForPrepareRetry(t, logs)
	require.NoError(t, prepareBlocker.Rollback().Error)
	select {
	case <-barrier.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Goose did not start its post-commit version read")
	}
	var committed bool
	require.NoError(t, admin.Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)
		AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='offline_upgrade_fence')`, storage.RelationalTopicPrepareVersion).Row().Scan(&committed))
	require.True(t, committed, "preparation must commit before the blocked version read")

	versionReadBlocker := admin.WithContext(ctx).Begin()
	require.NoError(t, versionReadBlocker.Error)
	t.Cleanup(func() { _ = versionReadBlocker.Rollback().Error })
	require.NoError(t, versionReadBlocker.Exec(`LOCK TABLE goose_db_version IN ACCESS EXCLUSIVE MODE`).Error)
	close(barrier.proceed)
	err := <-result
	require.NoError(t, versionReadBlocker.Rollback().Error)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, storage.IsLockNotAvailable(err))
	const reason = "feature_storage_relational_topics_prepare_deadline_exceeded: preparation outcome unconfirmed"
	require.Equal(t, reason, storage.MigrationFailureReason(err))
	var failures []logEntry
	for _, entry := range logs.entriesSnapshot() {
		if entry.message == "relational_topic_migration_failed" {
			failures = append(failures, entry)
		}
	}
	require.Len(t, failures, 1)
	require.Equal(t, reason, failures[0].attrs["failure_reason"])
	require.Equal(t, "unconfirmed", failures[0].attrs["transaction_outcome"])
	require.NotEqual(t, "55P03", failures[0].attrs["sqlstate"])
	require.NoError(t, RunUp(ctx, db, time.Minute, newRecordingLogger()))
	dbPool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, storage.ValidateStartupMigrationState(ctx, dbPool, storage.MigrationsDir()))
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
