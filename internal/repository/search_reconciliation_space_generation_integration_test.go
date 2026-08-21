package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEmbeddingReconciliationIgnoresSealedSpaceGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-sealed-space-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-sealed-space-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-sealed-space", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.New())
	require.NoError(t, err)
	sealedDocument, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "sealed private reconciliation candidate", SpaceID: privateSpace.ID.String(),
	})
	require.NoError(t, err)
	activeCanary := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "active reconciliation canary", 1)
	activeBacklog := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "active reconciliation backlog", 1)

	failedAt := time.Now().UTC().Add(-3 * time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, candidate := range []struct {
			documentID string
			failedAt   time.Time
		}{
			{sealedDocument.SearchDocumentID, failedAt},
			{activeCanary.SearchDocumentID, failedAt.Add(time.Minute)},
			{activeBacklog.SearchDocumentID, failedAt.Add(2 * time.Minute)},
		} {
			if err := tx.Exec(`
				UPDATE embedding_jobs
				SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
				    failure_class = 'transient', failure_code = 'provider_timeout',
				    first_failed_at = ?, last_failed_at = ?, completed_at = ?, error = 'timeout'
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, candidate.failedAt, candidate.failedAt, candidate.failedAt, teamID, candidate.documentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				UPDATE search_documents
				SET search_state = 'failed', embedding_error = 'timeout'
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, teamID, candidate.documentID).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))

	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC().Truncate(time.Minute), CreateIfMissing: true,
		WorkerID: "sealed-space-reconciliation-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)

	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.Equal(t, activeCanary.SearchDocumentID, canary.SearchDocumentID)
	require.NotEqual(t, privateSpace.ID.String(), canary.SpaceID)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID,
		WorkerID: run.WorkerID, LeaseToken: run.LeaseToken, AttemptedAt: time.Now().UTC(), Lease: time.Minute,
	}))
	require.NoError(t, repo.CompleteEmbeddingReconciliationCanary(ctx, CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: run.WorkerID,
		LeaseToken: run.LeaseToken, Succeeded: true, RecoveredCount: 1,
	}))

	requeued, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: run.WorkerID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: run.CandidateCutoff, BatchSize: 10, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)

	var sealedStatus, canaryStatus, backlogStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid),
				(SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid),
				(SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid)
		`, sealedDocument.QueuedJobID, canary.EmbeddingJobID, activeBacklog.QueuedJobID).Row().Scan(
			&sealedStatus, &canaryStatus, &backlogStatus,
		)
	}))
	require.Equal(t, string(domain.EmbeddingJobFailed), sealedStatus)
	require.Equal(t, string(domain.EmbeddingJobProcessing), canaryStatus)
	require.Equal(t, string(domain.EmbeddingJobQueued), backlogStatus)
}
