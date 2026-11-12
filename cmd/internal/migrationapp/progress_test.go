package migrationapp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
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
			progress.Report("dependency_closure", "evidence_only_retired_records", "started", "completed", "iterations", nil, nil)
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
