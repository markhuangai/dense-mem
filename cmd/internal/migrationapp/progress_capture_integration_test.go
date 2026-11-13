//go:build integration

package migrationapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

func testProgressCombinedCaptureFailure(t *testing.T) {
	for _, outcome := range []string{"committed", "rolled_back"} {
		t.Run(outcome, func(t *testing.T) {
			db, dsn := openMigrationProgressDB(t)
			migrator, err := postgres.NewMigrator(db)
			require.NoError(t, err)
			require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
			seedProgressMigration(t, db, 10, 10)
			before := progressEvidenceFingerprint(t, db)
			copyProgressMigration(t, func(s string) string {
				injection := `DO $fault$ BEGIN RAISE NOTICE 'dense_mem_migration_progress:%:{"body":"Exact progress Evidence. café 🧭 progress-credential-canary"}',current_setting('app.migration_execution_id'); END $fault$;
SELECT pg_advisory_xact_lock(752,1);`
				return strings.Replace(s, "DO $closure$", injection+"\nDO $closure$", 1)
			})
			migrator, err = postgres.NewMigrator(db)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			blocker, err := sqlDB.Conn(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { _ = blocker.Close() })
			_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_lock(752,1)`)
			require.NoError(t, err)
			logs := newProgressLog(t)
			ctx, cancel := context.WithCancel(withProgressSlot(context.Background()))
			defer cancel()
			var progress *postgres.MigrationProgress
			ctx, progress = postgres.ObserveMigration(ctx, func(s postgres.MigrationSnapshot) {
				logInfo(ctx, logs.logger, "evidence_only_migration_progress", progressAttrs(s)...)
			})
			defer progress.Close()
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				done <- migrateEvidenceOnly(ctx, db, migrator, logs.logger)
			}()
			t.Cleanup(func() {
				cancel()
				_, _ = blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(752,1)`)
				select {
				case <-finished:
				case <-time.After(20 * time.Second):
					t.Error("migration did not stop during fixture cleanup")
				}
			})
			require.Eventually(t, func() bool { return progress.Snapshot().CaptureReason == "invalid_notice" }, 20*time.Second, 10*time.Millisecond)
			for _, event := range logs.records(t) {
				require.NotEqual(t, "invalid_notice", event["capture_reason"], "the failure must precede another progress observation")
			}
			observationDB, err := postgres.Open(context.Background(), migrationProgressTestConfig{dsn})
			require.NoError(t, err)
			observationSQL, err := observationDB.DB()
			require.NoError(t, err)
			require.NoError(t, observationSQL.Close())
			for range 3 {
				heartbeatProgress(ctx, logs.logger, observationSQL, true)
			}
			heartbeatProgress(ctx, logs.logger, observationSQL, false)
			warnings := 0
			for _, event := range logs.records(t) {
				if event["msg"] == "evidence_only_migration_capture_incomplete" {
					warnings++
					require.Equal(t, "WARN", event["level"])
					require.Equal(t, "invalid_notice", event["capture_reason"])
					require.Equal(t, "incomplete", event["capture_state"])
					require.Equal(t, true, event["wait_observation_unavailable"])
					require.Equal(t, "database connection closed", event["wait_observation_failure_reason"])
					require.Equal(t, progress.Snapshot().ExecutionID, event["migration_execution_id"])
					encoded, err := json.Marshal(event)
					require.NoError(t, err)
					t.Logf("combined capture diagnostic: %s", encoded)
				}
			}
			require.Equal(t, 1, warnings)
			if outcome == "rolled_back" {
				cancel()
			} else {
				heartbeatProgress(ctx, logs.logger, sqlDB, true)
			}
			_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(752,1)`)
			require.NoError(t, err)
			select {
			case err := <-done:
				if outcome == "committed" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("migration did not finish after releasing the fixture lock")
			}
			require.Equal(t, before, progressEvidenceFingerprint(t, db))
			final, waitWarnings := 0, 0
			for _, event := range logs.records(t) {
				if event["wait_observation_failure_reason"] != nil {
					waitWarnings++
				}
				if event["msg"] != "evidence_only_migration_completed" && event["msg"] != "evidence_only_migration_failed" {
					continue
				}
				final++
				require.Equal(t, outcome, event["transaction_outcome"])
				require.Equal(t, "incomplete", event["capture_state"])
				require.Equal(t, "invalid_notice", event["capture_reason"])
				require.Equal(t, true, event["wait_observation_unavailable"])
				require.Equal(t, progress.Snapshot().ExecutionID, event["migration_execution_id"])
				encoded, err := json.Marshal(event)
				require.NoError(t, err)
				t.Logf("combined capture outcome: %s", encoded)
			}
			require.Equal(t, 1, final)
			require.Equal(t, 1, waitWarnings)
		})
	}
}
