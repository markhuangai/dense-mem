package migrationapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/observability"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
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
			logWarn(ctx, logger, "evidence_only_migration_capture_incomplete", progressAttrs(progress.Snapshot())...)
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
		{"ontology", fmt.Errorf("private-memory-canary: %w: 7 unreconciled attempts require terminal evidence", ontology.ErrMaintenanceUpgradeBlocked), "ontology upgrade blocked: unreconciled attempts require terminal evidence"},
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
