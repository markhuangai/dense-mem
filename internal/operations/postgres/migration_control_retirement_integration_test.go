//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestAuthorityRepositoryOperationLogRetentionSurvivesRepositoryRestart(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	testOperationLogRetentionSurvivesRepositoryRestart(t, appDB, rls)
}

func TestOperationLogRepositoryRetriesStableEventIDWithoutDuplication(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	repo := NewOperationLogRepository(appDB, rls)
	eventID := uuid.New()
	entry := domain.OperationLog{ID: eventID, Timestamp: time.Now().UTC(), Severity: "INFO", SeverityRank: 20, Message: "stable event", Source: "test"}
	require.NoError(t, repo.AppendBatch(context.Background(), []domain.OperationLog{entry}))
	require.NoError(t, repo.AppendBatch(context.Background(), []domain.OperationLog{entry}))
	page, err := repo.List(context.Background(), domain.OperationLogFilter{Limit: 100})
	require.NoError(t, err)
	count := 0
	for _, item := range page.Items {
		if item.ID == eventID {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestOperationLogSinkPoolAppendsAttributedEventWhileApplicationPoolIsHeld(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	appSQL.SetMaxOpenConns(1)
	held, err := appSQL.Conn(ctx)
	require.NoError(t, err)
	defer held.Close()

	dialector, ok := appDB.Dialector.(*gormpostgres.Dialector)
	require.True(t, ok)
	sinkClient, err := storagepostgres.OpenOperationLogClient(ctx, operationLogDSNConfig{dsn: dialector.Config.DSN}, nil)
	require.NoError(t, err)
	defer sinkClient.Close()

	teamID, profileID, eventID := uuid.New(), uuid.New(), uuid.New()
	repo := NewOperationLogRepository(sinkClient.GetDB(), rls)
	require.NoError(t, repo.AppendBatch(ctx, []domain.OperationLog{{
		ID: eventID, Timestamp: time.Now().UTC(), Severity: "INFO", SeverityRank: 20,
		Message: "dedicated sink write", TeamID: &teamID, ProfileID: &profileID,
	}}))

	var got struct {
		TeamID    string
		ProfileID string
		Count     int64
	}
	require.NoError(t, rls.WithSystemTx(ctx, sinkClient.GetDB(), func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT team_id::text, profile_id::text,
			       count(*) OVER ()
			FROM operation_logs
			WHERE id = ?
		`, eventID).Row().Scan(&got.TeamID, &got.ProfileID, &got.Count)
	}))
	assert.Equal(t, teamID.String(), got.TeamID)
	assert.Equal(t, profileID.String(), got.ProfileID)
	assert.EqualValues(t, 1, got.Count)
}

type operationLogDSNConfig struct{ dsn string }

func (c operationLogDSNConfig) GetPostgresDSN() string { return c.dsn }

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
