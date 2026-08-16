//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPruneTerminalEmbeddingJobsEnforcesCutoffStatusesAndSkipLocked(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "embedding-retention-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "embedding-retention-owner")
	insertSearchTestContract(t, adminDB, rls, "embedding-retention", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	old := now.Add(-embeddingJobRetentionAge - time.Second)
	boundary := now.Add(-embeddingJobRetentionAge)
	recent := boundary.Add(time.Second)

	type fixture struct {
		status      string
		completedAt *time.Time
		eligible    bool
	}
	fixtures := []fixture{
		{status: "completed", completedAt: &old, eligible: true},
		{status: "stale", completedAt: &old, eligible: true},
		{status: "cancelled", completedAt: &old, eligible: true},
		{status: "completed", completedAt: &boundary},
		{status: "completed", completedAt: &recent},
		{status: "failed", completedAt: &old},
		{status: "queued"},
		{status: "processing"},
	}
	jobIDs := make([]string, 0, len(fixtures))
	for index, item := range fixtures {
		document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "retention fixture "+uuid.NewString(), int64(index+1))
		jobIDs = append(jobIDs, document.QueuedJobID)
		require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE embedding_jobs
				SET status = ?, completed_at = ?, updated_at = ?
				WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
			`, item.status, item.completedAt, now, teamID, document.QueuedJobID).Error
		}))
	}

	lockTx := appDB.Begin()
	require.NoError(t, lockTx.Error)
	defer lockTx.Rollback()
	require.NoError(t, lockTx.Exec(`
		SELECT set_config('app.tx_mode', 'system', true),
		       set_config('app.current_team_id', '', true),
		       set_config('app.current_profile_id', '', true)
	`).Error)
	var lockedJobID string
	require.NoError(t, lockTx.Raw(`
		SELECT embedding_job_id::text
		FROM embedding_jobs
		WHERE team_id = ?::uuid
		  AND status IN ('completed', 'stale', 'cancelled')
		  AND completed_at < ?
		ORDER BY completed_at, team_id, embedding_job_id
		LIMIT 1
		FOR UPDATE
	`, teamID, boundary).Row().Scan(&lockedJobID))

	deleted, err := repo.PruneTerminalEmbeddingJobs(ctx, now, embeddingJobRetentionBatchSize)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.NoError(t, lockTx.Commit().Error)

	deleted, err = repo.PruneTerminalEmbeddingJobs(ctx, now, 1)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	var remaining []struct {
		EmbeddingJobID string
		Status         string
	}
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT embedding_job_id::text, status
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			ORDER BY embedding_job_id
		`, teamID).Scan(&remaining).Error
	}))
	require.Len(t, remaining, 5)
	remainingByID := make(map[string]string, len(remaining))
	for _, item := range remaining {
		remainingByID[item.EmbeddingJobID] = item.Status
	}
	for index, item := range fixtures {
		if item.eligible {
			require.NotContains(t, remainingByID, jobIDs[index])
			continue
		}
		require.Equal(t, item.status, remainingByID[jobIDs[index]])
	}
	require.NotEmpty(t, lockedJobID)
}
