//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestAuthorityRepositoryOperationLogRetentionSurvivesRepositoryRestart(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	testOperationLogRetentionSurvivesRepositoryRestart(t, appDB, rls)
}

func testOperationLogRetentionSurvivesRepositoryRestart(t *testing.T, appDB *gorm.DB, rls *storagepostgres.RLS) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO operation_logs (timestamp, severity, severity_rank, message, source, correlation_id, attrs)
			VALUES (?, 'INFO', 20, 'expired operation log', 'test', 'expired', '{}'::jsonb),
			       (?, 'INFO', 20, 'retained operation log', 'test', 'retained', '{}'::jsonb)
		`, old, recent).Error
	}))

	repo := NewOperationLogRepository(appDB, rls)
	require.NoError(t, repo.PruneBefore(ctx, now.Add(-24*time.Hour)))
	require.NoError(t, repo.AppendBatch(ctx, []domain.OperationLog{{
		Timestamp: now, Severity: "INFO", SeverityRank: 20, Message: "flushed before restart", Source: "test",
	}}))

	restarted := NewOperationLogRepository(appDB, rls)
	page, err := restarted.List(ctx, domain.OperationLogFilter{Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	messages := map[string]bool{}
	for _, item := range page.Items {
		messages[item.Message] = true
	}
	require.False(t, messages["expired operation log"])
	require.True(t, messages["retained operation log"])
	require.True(t, messages["flushed before restart"])
}
