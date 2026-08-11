package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gorm.io/gorm"
)

func TestSearchRenewEmbeddingJobLeaseUsesWorkerAndAttemptFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-renew-lease-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-renew-lease-owner")
	insertSearchTestContract(t, adminDB, rls, "search-renew-lease", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	_ = upsertSearchDocumentForTest(t, repo, teamID, ownerID, "slow provider text", 1)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{TeamID: teamID, WorkerID: "renew-worker", Limit: 1, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	var before, after time.Time
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT lease_until FROM embedding_jobs WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, claimed[0].EmbeddingJobID).Row().Scan(&before)
	}))
	require.NoError(t, repo.RenewEmbeddingJobLease(ctx, RenewEmbeddingJobLeaseInput{TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "renew-worker", ExpectedAttempts: claimed[0].Attempts, Lease: 5 * time.Minute}))
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT lease_until FROM embedding_jobs WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, claimed[0].EmbeddingJobID).Row().Scan(&after)
	}))
	assert.True(t, after.After(before), "renewal should extend the active lease")

	err = repo.RenewEmbeddingJobLease(ctx, RenewEmbeddingJobLeaseInput{TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "other-worker", ExpectedAttempts: claimed[0].Attempts, Lease: time.Minute})
	require.ErrorIs(t, err, ErrEmbeddingLeaseLost)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE embedding_jobs SET lease_until = clock_timestamp() - interval '1 second' WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, claimed[0].EmbeddingJobID).Error
	}))
	err = repo.RenewEmbeddingJobLease(ctx, RenewEmbeddingJobLeaseInput{TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "renew-worker", ExpectedAttempts: claimed[0].Attempts, Lease: time.Minute})
	require.ErrorIs(t, err, ErrEmbeddingLeaseLost)
}
