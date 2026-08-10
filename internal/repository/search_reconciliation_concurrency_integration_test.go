package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEmbeddingReconciliationKeepsIncidentOpenForPostCutoffFailure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-new-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-new-failure-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-new-failure", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	backlog := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "pre-cutoff failure", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, backlog.SearchDocumentID, "backlog-failure-writer")
	cutoff := databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: cutoff, WorkerID: "new-failure-reconciler", Lease: time.Minute, Now: cutoff,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	newFailure := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "post-cutoff failure", 1)
	claimedJobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "new-failure-writer", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedJobs, 1)
	require.Equal(t, newFailure.SearchDocumentID, claimedJobs[0].SearchDocumentID)

	writerTx := appDB.WithContext(ctx).Begin()
	require.NoError(t, writerTx.Error)
	defer writerTx.Rollback()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"SELECT set_config('app.current_team_id', ?, true)", []any{teamID}},
		{"SELECT set_config('app.current_profile_id', ?, true)", []any{ownerID}},
		{"SELECT set_config('app.tx_mode', 'team', true)", nil},
		{"SELECT set_config('app.embedding_job_failure_writer', 'current', true)", nil},
	} {
		require.NoError(t, writerTx.Exec(statement.query, statement.args...).Error)
	}
	require.NoError(t, lockEmbeddingFailureIncident(ctx, writerTx, teamID, "evidence", contract.EmbeddingContractID,
		string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderTimeout), contract.EmbeddingDimensions))
	require.NoError(t, writerTx.Exec(`
		UPDATE embedding_jobs
		SET status = 'failed', error = 'embedding provider timed out',
		    failure_class = 'transient', failure_code = 'provider_timeout',
		    first_failed_at = clock_timestamp(), last_failed_at = clock_timestamp(),
		    completed_at = clock_timestamp(), lease_until = NULL, worker_id = '', updated_at = now()
		WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		  AND worker_id = 'new-failure-writer' AND status = 'processing'
	`, teamID, claimedJobs[0].EmbeddingJobID).Error)

	requeueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type requeueResult struct {
		count int64
		err   error
	}
	requeueResults := make(chan requeueResult, 1)
	go func() {
		count, requeueErr := repo.RequeueEmbeddingReconciliationJobs(requeueCtx, RequeueEmbeddingReconciliationJobsInput{
			RunID: run.RunID, WorkerID: "new-failure-reconciler", LeaseToken: run.LeaseToken,
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
			CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
		})
		requeueResults <- requeueResult{count: count, err: requeueErr}
	}()
	requireEmbeddingFailureIncidentWaiter(t, ctx, adminDB, rls, teamID, "evidence", contract.EmbeddingContractID,
		string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderTimeout), contract.EmbeddingDimensions)
	require.NoError(t, writerTx.Commit().Error)
	requeue := <-requeueResults
	require.NoError(t, requeue.err)
	require.EqualValues(t, 1, requeue.count)

	var backlogStatus, newFailureStatus, incidentStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, backlog.QueuedJobID).Row().Scan(&backlogStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, newFailure.QueuedJobID).Row().Scan(&newFailureStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status
			FROM embedding_failure_incidents
			WHERE team_id = ?::uuid AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ? AND source_kind = 'evidence'
			  AND failure_class = 'transient' AND failure_code = 'provider_timeout'
		`, teamID, contract.EmbeddingContractID, contract.EmbeddingDimensions).Row().Scan(&incidentStatus)
	}))
	require.Equal(t, "queued", backlogStatus)
	require.Equal(t, "failed", newFailureStatus)
	require.Equal(t, "open", incidentStatus)
}

func TestEmbeddingReconciliationLocksDocumentBeforeJob(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-lock-order-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-lock-order-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-lock-order", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "lock order before", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, document.SearchDocumentID, "lock-order-failure-writer")
	cutoff := databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: cutoff, WorkerID: "lock-order-reconciler", Lease: time.Minute, Now: cutoff,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	placementLocked := make(chan error, 1)
	releasePlacement := make(chan struct{}, 1)
	placementResult := make(chan error, 1)
	var updated *SearchDocumentResult
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: document.SourceKind, SourceID: document.SourceID,
		SourceVersion: 2, ProjectionFormat: document.ProjectionFormat,
		DocumentText: "lock order after",
	})
	go func() {
		placementResult <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			var locked int
			lockErr := tx.Raw(`
				SELECT 1
				FROM search_documents
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
				FOR UPDATE
			`, teamID, document.SearchDocumentID).Row().Scan(&locked)
			placementLocked <- lockErr
			if lockErr != nil {
				return lockErr
			}
			select {
			case <-releasePlacement:
			case <-ctx.Done():
				return ctx.Err()
			}
			var upsertErr error
			updated, upsertErr = upsertSearchDocumentInTx(ctx, tx, input, contract, defaultEmbeddingJobMaxAttempts)
			return upsertErr
		})
	}()
	require.NoError(t, <-placementLocked)

	requeueCtx, requeueCancel := context.WithTimeout(ctx, 2*time.Second)
	count, requeueErr := repo.RequeueEmbeddingReconciliationJobs(requeueCtx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: "lock-order-reconciler", LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
	})
	requeueCancel()
	releasePlacement <- struct{}{}
	require.NoError(t, <-placementResult)
	require.NoError(t, requeueErr)
	require.Zero(t, count)
	require.NotNil(t, updated)
	require.NotEmpty(t, updated.QueuedJobID)

	var oldStatus, newStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, document.QueuedJobID).Row().Scan(&oldStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, updated.QueuedJobID).Row().Scan(&newStatus)
	}))
	require.Equal(t, "stale", oldStatus)
	require.Equal(t, "queued", newStatus)
}

func failEmbeddingDocumentForReconciliationTest(
	t *testing.T,
	ctx context.Context,
	repo *SearchRepositoryImpl,
	teamID string,
	documentID string,
	workerID string,
) {
	t.Helper()
	jobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: workerID, Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, documentID, jobs[0].SearchDocumentID)
	result, err := repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: jobs[0].EmbeddingJobID,
		WorkerID: workerID, ExpectedAttempts: jobs[0].Attempts,
		Error:        "embedding provider timed out",
		FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode:  string(domain.EmbeddingFailureProviderTimeout),
		Terminal:     true,
	})
	require.NoError(t, err)
	require.True(t, result.Terminal)
}

func requireEmbeddingFailureIncidentWaiter(
	t *testing.T,
	ctx context.Context,
	adminDB *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID string,
	sourceKind string,
	contractID string,
	failureClass string,
	failureCode string,
	dimensions int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int64
		lockErr := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Raw(`
				WITH expected AS (
					SELECT hashtextextended(
						concat_ws('|', ?::uuid::text, ?::uuid::text, ?::integer::text, ?::text, ?::text, ?::text), 0
					) AS lock_id
				)
				SELECT count(*)
				FROM pg_locks
				CROSS JOIN expected
				WHERE locktype = 'advisory' AND NOT granted
				  AND objsubid = 1
				  AND classid::bigint = ((expected.lock_id >> 32) & 4294967295)
				  AND objid::bigint = (expected.lock_id & 4294967295)
			`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode).Scan(&waiting).Error
		})
		if lockErr == nil && waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected transaction did not wait for the failure-writer incident lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
