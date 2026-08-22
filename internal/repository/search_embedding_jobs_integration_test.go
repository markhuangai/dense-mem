package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
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

func TestSearchClaimEmbeddingJobsSeesPrivateSpaceWithoutRequestActor(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-private-space-claim-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-private-space-claim-owner")
	insertSearchTestContract(t, adminDB, rls, "search-private-space-claim", 3, "exact", "")

	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT memory_space_id::text, space.generation
			FROM credentials AS credential
			JOIN memory_spaces AS space ON space.id = credential.memory_space_id
			WHERE credential.id = ?::uuid
		`, ownerID).Row().Scan(&spaceID, &generation)
	}))

	repo := NewSearchRepository(appDB, rls)
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "private space embedding claim",
		SpaceID: spaceID, SpaceGeneration: generation,
	})
	require.NoError(t, err)

	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "private-space-claim-worker", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, spaceID, claimed[0].SpaceID)
	require.Equal(t, generation, claimed[0].SpaceGeneration)
}

func TestSearchEmbeddingJobFinalizationFencesSpaceID(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-space-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-space-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-space-fence", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	for _, text := range []string{"renew space fence", "complete space fence", "fail space fence"} {
		_ = upsertSearchDocumentForTest(t, repo, teamID, ownerID, text, 1)
	}
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{TeamID: teamID, WorkerID: "space-fence-worker", Limit: 3, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, claimed, 3)
	wrongSpace := uuid.NewString()

	err = repo.RenewEmbeddingJobLease(ctx, RenewEmbeddingJobLeaseInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[0].Attempts, Lease: time.Minute, SpaceID: wrongSpace,
	})
	require.ErrorIs(t, err, ErrEmbeddingLeaseLost)
	require.NoError(t, repo.RenewEmbeddingJobLease(ctx, RenewEmbeddingJobLeaseInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[0].Attempts, Lease: time.Minute, SpaceID: claimed[0].SpaceID,
	}))

	err = repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[1].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[1].Attempts, Embedding: []float32{1, 0, 0}, SpaceID: wrongSpace,
	})
	require.ErrorIs(t, err, ErrEmbeddingLeaseLost)
	require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[1].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[1].Attempts, Embedding: []float32{1, 0, 0}, SpaceID: claimed[1].SpaceID,
	}))

	failed, err := repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[2].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[2].Attempts, FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode: string(domain.EmbeddingFailureProviderTimeout), Terminal: true, SpaceID: wrongSpace,
	})
	require.ErrorIs(t, err, ErrEmbeddingLeaseLost)
	assert.Nil(t, failed)
	failed, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[2].EmbeddingJobID, WorkerID: "space-fence-worker",
		ExpectedAttempts: claimed[2].Attempts, FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode: string(domain.EmbeddingFailureProviderTimeout), Terminal: true, SpaceID: claimed[2].SpaceID,
	})
	require.NoError(t, err)
	require.NotNil(t, failed)
	assert.True(t, failed.Terminal)
}
