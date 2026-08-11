package repository

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchClaimEmbeddingJobsPreservesTimeoutClassificationAfterLeaseExpiry(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-expired-timeout-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-expired-timeout-owner")
	insertSearchTestContract(t, adminDB, rls, "search-expired-timeout", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	doc := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "expired timeout embedding job", 1)

	firstClaim, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "worker-timeout-first", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: firstClaim[0].EmbeddingJobID,
		WorkerID: "worker-timeout-first", ExpectedAttempts: firstClaim[0].Attempts,
		Error:        "embedding request timed out: context deadline exceeded",
		FailureClass: string(domain.EmbeddingFailureTransient), FailureCode: string(domain.EmbeddingFailureProviderTimeout),
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET available_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, firstClaim[0].EmbeddingJobID).Error
	}))

	secondClaim, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "worker-timeout-second", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, secondClaim, 1)
	require.Equal(t, doc.SearchDocumentID, secondClaim[0].SearchDocumentID)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET max_attempts = attempts, lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, secondClaim[0].EmbeddingJobID).Error
	}))

	nextClaim, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "worker-timeout-cleanup", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Empty(t, nextClaim)

	var status, failureClass, failureCode string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, failure_class, failure_code
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, secondClaim[0].EmbeddingJobID).Row().Scan(&status, &failureClass, &failureCode)
	}))
	assert.Equal(t, string(domain.EmbeddingJobFailed), status)
	assert.Equal(t, string(domain.EmbeddingFailureTransient), failureClass)
	assert.Equal(t, string(domain.EmbeddingFailureProviderTimeout), failureCode)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Len(t, convergence.FailureGroups, 1)
	assert.Equal(t, "attention_required", convergence.FailureGroups[0].Status)
	assert.EqualValues(t, 1, convergence.FailureGroups[0].FailedJobCount)
}
