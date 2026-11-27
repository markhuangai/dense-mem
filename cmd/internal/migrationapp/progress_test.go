package migrationapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestMigrationProgressRealLoggerAndUnknownCounts(t *testing.T) {
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelInfo, slog.LevelWarn} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer
			root := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level})).With(observability.String("canary", "Bearer configured-canary"))
			logger := root.(*observability.Logger).Slog()
			ctx := context.Background()
			_, progress := postgres.ObserveMigration(ctx, func(s postgres.MigrationSnapshot) {
				logInfo(ctx, logger, "evidence_only_migration_progress", progressAttrs(s)...)
			})
			defer progress.Close()
			progress.Report("ontology_reset", "ontology_catalog_heads", "started", "affected", "rows", nil, nil)
			progress.Incomplete("notice_sequence_gap")
			require.True(t, progress.MarkWaitObservationUnavailable())
			logWarn(ctx, logger, "evidence_only_migration_capture_incomplete", append(progressAttrs(progress.Snapshot()),
				"wait_observation_failure_reason", postgres.MigrationFailureReason(sql.ErrConnDone))...)
			require.NotContains(t, output.String(), "configured-canary")
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if level == slog.LevelWarn {
				require.Len(t, lines, 1)
			} else {
				require.Len(t, lines, 2)
			}
			for _, line := range lines {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				require.Equal(t, "unknown", event["total_kind"])
				require.Nil(t, event["remaining"])
				require.Nil(t, event["eta_seconds"])
				require.Equal(t, "transaction_local", event["work_durability"])
				require.NotEmpty(t, event["migration_execution_id"])
				if event["level"] == "WARN" {
					require.Equal(t, "incomplete", event["capture_state"])
					require.Equal(t, "notice_sequence_gap", event["capture_reason"])
					require.Equal(t, true, event["wait_observation_unavailable"])
					require.Equal(t, "database connection closed", event["wait_observation_failure_reason"])
				}
			}
		})
	}
}

func TestMigrationProgressLegacyFailureRealLogger(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"identity", &pgconn.PgError{Code: "P0001", Message: "identity cleanup blocked: usage history missing ownership aliases (7 rows)", Detail: "private-memory-canary"}, "identity cleanup blocked: usage history missing ownership aliases"},
		{"lock timeout", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout", Where: "private-memory-canary"}, "lock timeout"},
		{"ontology", fmt.Errorf("private-memory-canary: %w: 7 unreconciled attempts require terminal evidence", topic.ErrMaintenanceUpgradeBlocked), "ontology upgrade blocked: unreconciled attempts require terminal evidence"},
		{"unknown", errors.New("private-memory-canary migration-credential-canary"), "migration operation failed; database payload omitted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx := observability.WithAuthenticationSecrets(context.Background(), "migration-credential-canary")
			reason := postgres.MigrationFailureReason(test.err)
			failure := &postgres.MigrationFailure{Cause: test.err, Reason: reason}
			logError(ctx, logger.Slog(), "postgres migrations failed", "error", reason, "credential_probe", "migration-credential-canary")
			logger.ErrorContext(ctx, "startup failed", failure, observability.String("credential_probe", "migration-credential-canary"))
			require.NotContains(t, output.String(), "private-memory-canary")
			require.NotContains(t, output.String(), "migration-credential-canary")
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			require.Len(t, lines, 2)
			for i, line := range lines {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				require.Equal(t, "ERROR", event["level"])
				want := test.want
				if i == 1 {
					want = "evidence_only_migration_failed: " + want
				}
				require.Equal(t, want, event["error"])
			}
		})
	}
}

func TestMigrationProgressTransactionOutcome(t *testing.T) {
	for _, test := range []struct {
		name, xid, status   string
		applied, compatible bool
		want                string
	}{
		{"aborted despite a later starter committing", "42", "aborted", true, true, "rolled_back"},
		{"rollback still in progress", "42", "in progress", false, false, "unconfirmed"},
		{"confirmed own commit", "42", "committed", true, true, "committed"},
		{"commit lacks receipt", "42", "committed", true, false, "unconfirmed"},
		{"expired transaction status", "42", "", true, true, "unconfirmed"},
		{"verified prior commit", "", "", true, true, "committed"},
		{"not started", "", "", false, false, "not_started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, evidenceOnlyTransactionOutcome(test.applied, test.compatible, test.xid, test.status))
		})
	}
}

func TestTopicMaintenanceUpgradeCleanupRealLogger(t *testing.T) {
	const canary = "topic-cleanup-authentication-canary"
	const payload = "private-database-payload-canary"
	for _, tc := range []struct {
		level string
		want  []string
	}{
		{"TRACE", []string{"INFO", "WARN", "ERROR"}},
		{"INFO", []string{"INFO", "WARN", "ERROR"}},
		{"WARN", []string{"WARN", "ERROR"}},
		{"ERROR", []string{"ERROR"}},
	} {
		t.Run(tc.level, func(t *testing.T) {
			level, err := observability.ParseLevel(tc.level)
			require.NoError(t, err)
			var output bytes.Buffer
			root := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			logger := root.With(observability.String("credential_probe", canary)).(*observability.Logger).Slog()
			ctx := observability.WithAuthenticationSecrets(t.Context(), canary)
			clean := postgres.TopicMaintenanceCleanupSummary{
				Executed: true, ExecutionID: "test-migration", RelationPrefix: "topic", CurrentVersion: 20261124010004,
				TargetVersion: 20261127010000, Elapsed: 5 * time.Millisecond, TransactionOutcome: "committed",
			}
			dirty := clean
			dirty.DiscardedBatches, dirty.ClearedTeamLeases, dirty.ClearedCommunityLeases, dirty.ClearedConsolidationLeases, dirty.ReconciledAttempts = 1, 2, 3, 4, 5
			failed := clean
			failed.Executed, failed.Phase, failed.TransactionOutcome, failed.SQLState = false, "cleanup_tx", "rolled_back", "55P03"
			failed.Err = &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout", Detail: payload}

			reportTopicMaintenanceCleanup(ctx, logger, postgres.TopicMaintenanceCleanupSummary{})
			reportTopicMaintenanceCleanup(ctx, logger, clean)
			reportTopicMaintenanceCleanup(ctx, logger, dirty)
			reportTopicMaintenanceCleanup(ctx, logger, failed)
			require.NotContains(t, output.String(), canary)
			require.NotContains(t, output.String(), payload)
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			require.Len(t, lines, len(tc.want))
			for i, line := range lines {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				require.Equal(t, tc.want[i], event["level"])
				require.Equal(t, "test-migration", event["migration_execution_id"])
				require.Equal(t, "topic", event["relation_prefix"])
				require.Equal(t, float64(20261124010004), event["current_version"])
				require.Equal(t, float64(20261127010000), event["target_version"])
				require.Equal(t, float64(5), event["elapsed_ms"])
				if tc.want[i] == "ERROR" {
					require.Equal(t, "topic_maintenance_upgrade_cleanup_failed", event["msg"])
					require.Equal(t, "cleanup_tx", event["phase"])
					require.Equal(t, "lock timeout", event["failure_reason"])
					require.Equal(t, "55P03", event["sqlstate"])
					require.Equal(t, "rolled_back", event["transaction_outcome"])
					require.Equal(t, "stop_active_writers_and_retry_upgrade", event["recovery_action"])
					continue
				}
				require.Equal(t, "topic_maintenance_upgrade_cleanup", event["msg"])
				require.Equal(t, "committed", event["transaction_outcome"])
				for pos, key := range []string{"discarded_batches", "cleared_team_leases", "cleared_community_leases", "cleared_consolidation_leases", "reconciled_attempts"} {
					want := 0
					if tc.want[i] == "WARN" {
						want = pos + 1
					}
					require.Equal(t, float64(want), event[key], key)
				}
			}
		})
	}
}
