//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
)

func TestFeatureStoragePostgresRetentionCountsAndConfigurationFailure(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	var output bytes.Buffer
	root := observability.NewConsoleWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	root = root.With(observability.String("fixture_auth", "fixture-operation-credential")).(*observability.Logger)
	ctx := observability.WithAuthenticationSecrets(context.Background(), "fixture-operation-credential")
	repo := NewOperationLogRepository(app, rls)
	service := operations.NewOperationLogService(repo, settings.NewAppConfigService(settingspostgres.NewAppConfigRepository(app, rls), nil))
	service.SetPruneLogger(root.ContextSlog())
	require.NoError(t, root.AttachSink(service))
	old := domain.OperationLog{Timestamp: time.Now().UTC().AddDate(0, 0, -31), Severity: "INFO", SeverityRank: 20, Message: "expired authored fixture", Source: "fixture"}
	recent := old
	recent.Timestamp = time.Now().UTC()
	recent.Message = "retained authored fixture"
	require.NoError(t, repo.AppendBatch(ctx, []domain.OperationLog{old, recent}))
	require.NoError(t, service.Prune(ctx))
	require.NoError(t, service.Flush(ctx))
	page, err := repo.List(ctx, domain.OperationLogFilter{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NoError(t, repo.AppendBatch(ctx, []domain.OperationLog{old}))
	require.NoError(t, admin.Exec(`REVOKE SELECT ON app_config FROM densemem_rls_test`).Error)
	t.Cleanup(func() { require.NoError(t, admin.Exec(`GRANT SELECT ON app_config TO densemem_rls_test`).Error) })
	service = operations.NewOperationLogService(repo, settings.NewAppConfigService(settingspostgres.NewAppConfigRepository(app, rls), nil))
	service.SetPruneLogger(root.ContextSlog())
	require.ErrorContains(t, service.Prune(ctx), "read operation log retention")
	page, err = repo.List(ctx, domain.OperationLogFilter{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.NoError(t, admin.Exec(`GRANT SELECT ON app_config TO densemem_rls_test`).Error)
	require.NotContains(t, output.String(), "fixture-operation-credential")
	var success, failure map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["msg"] == "operation_log_prune_completed" {
			success = record
		}
		if record["msg"] == "operation_log_prune_failed" {
			failure = record
		}
	}
	require.Equal(t, float64(1), success["deleted_rows"])
	require.Equal(t, float64(30), success["retention_days"])
	require.Equal(t, "ERROR", failure["level"])
	require.Equal(t, false, failure["retention_available"])
	require.Equal(t, float64(0), failure["deleted_rows"])
}
