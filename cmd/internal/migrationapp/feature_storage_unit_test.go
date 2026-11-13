package migrationapp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

func TestFeatureStorageUnitCommittedAndFailedLogsUseRealProtection(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := observability.WithAuthenticationSecrets(context.Background(), "fixture-operational-credential")
	ctx, progress := postgres.ObserveMigration(ctx, func(s postgres.MigrationSnapshot) { reportFeatureStorageProgress(ctx, logger.Slog(), s) })
	defer progress.Close()
	progress.SetMigrationVersion(postgres.FeatureStorageDataVersion)
	progress.FeatureStorageCheckpoint("copy", "ontology_record_revisions", "batch_committed", 100, 1, "sha256:checkpoint", "sha256:inventory", "fixture-operational-credential", "sha256:schema")
	progress.FeatureStorageFailure("failed", "feature_storage_destination_payload_mismatch")
	require.NotContains(t, output.String(), "fixture-operational-credential")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 2)
	var committed, failed map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &committed))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &failed))
	require.Equal(t, float64(100), committed["committed_rows"])
	require.Equal(t, "committed_checkpoints", committed["work_durability"])
	require.Equal(t, "ERROR", failed["level"])
	require.Equal(t, "incomplete", failed["capture_state"])
	require.Equal(t, "feature_storage_destination_payload_mismatch", failed["failure_reason"])
}

func TestFeatureStorageUnitSeverityFiltersCommittedProgress(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))
	_, progress := postgres.ObserveMigration(context.Background(), func(s postgres.MigrationSnapshot) {
		reportFeatureStorageProgress(context.Background(), logger.Slog(), s)
	})
	defer progress.Close()
	progress.SetMigrationVersion(postgres.FeatureStorageDataVersion)
	progress.FeatureStorageCheckpoint("copy", "ontology_record_revisions", "batch_committed", 100, 1, "checkpoint", "inventory", "source", "schema")
	progress.FeatureStorageFailure("failed", "feature_storage_database_failure")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 1)
	var failed map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &failed))
	require.Equal(t, "ERROR", failed["level"])
	require.Equal(t, "feature_storage_migration_failed", failed["msg"])
}

func TestFeatureStorageUnitCaptureGapSurvivesCommittedCutover(t *testing.T) {
	var output bytes.Buffer
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_, progress := postgres.ObserveMigration(context.Background(), func(s postgres.MigrationSnapshot) {
		reportFeatureStorageProgress(context.Background(), logger.Slog(), s)
	})
	defer progress.Close()
	progress.SetMigrationVersion(postgres.FeatureStorageCutoverVersion)
	require.True(t, progress.MarkWaitObservationUnavailable())
	progress.BindFeatureTransaction(123, time.Now().UTC())
	progress.FeatureStorageCheckpoint("cutover", "catalog", "completed", 210, 3, "checkpoint", "inventory", "source", "schema")
	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event))
	require.Equal(t, "committed", event["transaction_outcome"])
	require.Equal(t, "incomplete", event["capture_state"])
	require.Equal(t, "wait_observation_unavailable", event["capture_reason"])
	require.Equal(t, true, event["wait_observation_unavailable"])
}
