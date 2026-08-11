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

func TestEmbeddingReconciliationLeavesPostCutoffFailureAttentionRequired(t *testing.T) {
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
		LocalRunDate: cutoff, CreateIfMissing: true, WorkerID: "new-failure-reconciler", Lease: time.Minute,
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

	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimedJobs[0].EmbeddingJobID,
		WorkerID: "new-failure-writer", ExpectedAttempts: claimedJobs[0].Attempts,
		Error: "embedding provider timed out", FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode: string(domain.EmbeddingFailureProviderTimeout), Terminal: true,
	})
	require.NoError(t, err)
	requeued, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: "new-failure-reconciler", LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)

	var backlogStatus, newFailureStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, backlog.QueuedJobID).Row().Scan(&backlogStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, newFailure.QueuedJobID).Row().Scan(&newFailureStatus)
	}))
	require.Equal(t, "queued", backlogStatus)
	require.Equal(t, "failed", newFailureStatus)
	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "attention_required", projection.Status)
	require.Len(t, projection.FailureGroups, 1)
	require.EqualValues(t, 1, projection.FailureGroups[0].FailedJobCount)
	require.EqualValues(t, 1, projection.FailureGroups[0].QueuedJobCount)
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
		LocalRunDate: cutoff, CreateIfMissing: true, WorkerID: "lock-order-reconciler", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run.LeaseUntil)
	initialLeaseUntil := *run.LeaseUntil

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

	requeueCtx, requeueCancel := context.WithTimeout(ctx, 10*time.Second)
	defer requeueCancel()
	type requeueResult struct {
		count int64
		err   error
	}
	requeueResults := make(chan requeueResult, 1)
	go func() {
		count, requeueErr := repo.RequeueEmbeddingReconciliationJobs(requeueCtx, RequeueEmbeddingReconciliationJobsInput{
			RunID: run.RunID, WorkerID: "lock-order-reconciler", LeaseToken: run.LeaseToken,
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
			CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
		})
		requeueResults <- requeueResult{count: count, err: requeueErr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case result := <-requeueResults:
			t.Fatalf("reconciliation completed before the locked document was released: count=%d err=%v", result.count, result.err)
		default:
		}
		var leaseUntil time.Time
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT lease_until FROM embedding_reconciliation_runs WHERE reconciliation_run_id = ?::uuid`, run.RunID).Row().Scan(&leaseUntil)
		}))
		if leaseUntil.After(initialLeaseUntil) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconciliation did not scan the locked document")
		}
		time.Sleep(10 * time.Millisecond)
	}
	releasePlacement <- struct{}{}
	require.NoError(t, <-placementResult)
	select {
	case result := <-requeueResults:
		require.NoError(t, result.err)
		require.Zero(t, result.count)
	case <-requeueCtx.Done():
		t.Fatal("reconciliation did not revisit the released document")
	}
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

func TestEmbeddingReconciliationRevisitsLockedCandidates(t *testing.T) {
	for _, lockTarget := range []string{"document", "job"} {
		t.Run(lockTarget, func(t *testing.T) {
			adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			ctx := context.Background()
			teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-locked-"+lockTarget+"-team")
			ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-locked-"+lockTarget+"-owner")
			insertSearchTestContract(t, adminDB, rls, "reconciliation-locked-"+lockTarget, 3, "exact", "")
			repo := NewSearchRepository(appDB, rls)
			contract, err := repo.GetActiveSearchContract(ctx)
			require.NoError(t, err)
			oldest := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "locked oldest failure", 1)
			failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, oldest.SearchDocumentID, "locked-oldest-writer")
			later := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "unlocked later failure", 1)
			failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, later.SearchDocumentID, "unlocked-later-writer")
			orderingTime := databaseNowForTest(t, adminDB, rls)
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Exec(`
					UPDATE embedding_jobs
					SET last_failed_at = CASE embedding_job_id
					    WHEN ?::uuid THEN ?::timestamptz
					    ELSE ?::timestamptz
					END
					WHERE embedding_job_id IN (?::uuid, ?::uuid)
				`, oldest.QueuedJobID, orderingTime.Add(-2*time.Minute), orderingTime.Add(-time.Minute),
					oldest.QueuedJobID, later.QueuedJobID).Error
			}))
			run, reserved, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
				EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
				LocalRunDate: orderingTime, CreateIfMissing: true, WorkerID: "locked-candidate-reconciler", Lease: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			lockTx := appDB.WithContext(ctx).Begin()
			require.NoError(t, lockTx.Error)
			defer lockTx.Rollback()
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{"SELECT set_config('app.current_team_id', ?, true)", []any{teamID}},
				{"SELECT set_config('app.current_profile_id', ?, true)", []any{ownerID}},
				{"SELECT set_config('app.tx_mode', 'team', true)", nil},
			} {
				require.NoError(t, lockTx.Exec(statement.query, statement.args...).Error)
			}
			lockQuery := `SELECT 1 FROM embedding_jobs WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid FOR UPDATE`
			lockID := oldest.QueuedJobID
			if lockTarget == "document" {
				lockQuery = `SELECT 1 FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ?::uuid FOR UPDATE`
				lockID = oldest.SearchDocumentID
			}
			var locked int
			require.NoError(t, lockTx.Raw(lockQuery, teamID, lockID).Row().Scan(&locked))

			requeueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			type requeueResult struct {
				count int64
				err   error
			}
			results := make(chan requeueResult, 1)
			go func() {
				count, requeueErr := repo.RequeueEmbeddingReconciliationJobs(requeueCtx, RequeueEmbeddingReconciliationJobsInput{
					RunID: run.RunID, WorkerID: "locked-candidate-reconciler", LeaseToken: run.LeaseToken,
					EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
					CandidateCutoff: run.CandidateCutoff, BatchSize: 1, Lease: time.Minute,
				})
				results <- requeueResult{count: count, err: requeueErr}
			}()

			deadline := time.Now().Add(5 * time.Second)
			for {
				select {
				case result := <-results:
					t.Fatalf("reconciliation completed before revisiting the locked %s: count=%d err=%v", lockTarget, result.count, result.err)
				default:
				}
				var status string
				require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, later.QueuedJobID).Row().Scan(&status)
				}))
				if status == "queued" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("reconciliation did not scan past the locked candidate")
				}
				time.Sleep(10 * time.Millisecond)
			}
			require.NoError(t, lockTx.Rollback().Error)

			select {
			case result := <-results:
				require.NoError(t, result.err)
				require.EqualValues(t, 2, result.count)
			case <-requeueCtx.Done():
				t.Fatal("reconciliation did not revisit the released candidate")
			}

			var oldestStatus, laterStatus string
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, oldest.QueuedJobID).Row().Scan(&oldestStatus); err != nil {
					return err
				}
				return tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, later.QueuedJobID).Row().Scan(&laterStatus)
			}))
			require.Equal(t, "queued", oldestStatus)
			require.Equal(t, "queued", laterStatus)
		})
	}
}

func TestEmbeddingFinalizationLocksDocumentBeforeJob(t *testing.T) {
	finalizers := []struct {
		name string
		run  func(context.Context, *SearchRepositoryImpl, EmbeddingJob, string) error
	}{
		{name: "complete", run: func(ctx context.Context, repo *SearchRepositoryImpl, job EmbeddingJob, workerID string) error {
			return repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
				TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
				ExpectedAttempts: job.Attempts, Embedding: []float32{1, 0, 0},
			})
		}},
		{name: "fail", run: func(ctx context.Context, repo *SearchRepositoryImpl, job EmbeddingJob, workerID string) error {
			_, err := repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
				TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
				ExpectedAttempts: job.Attempts, FailureClass: string(domain.EmbeddingFailureTransient),
				FailureCode: string(domain.EmbeddingFailureProviderTimeout), Terminal: true,
			})
			return err
		}},
	}
	for _, finalizer := range finalizers {
		t.Run(finalizer.name, func(t *testing.T) {
			adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			teamID := createLedgerTeam(t, adminDB, rls, "embedding-finalization-lock-"+finalizer.name)
			ownerID := createLedgerProfile(t, adminDB, rls, teamID, "embedding-finalization-owner-"+finalizer.name)
			insertSearchTestContract(t, adminDB, rls, "embedding-finalization-"+finalizer.name, 3, "exact", "")
			repo := NewSearchRepository(appDB, rls)
			contract, err := repo.GetActiveSearchContract(ctx)
			require.NoError(t, err)
			document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "embedding finalization before "+finalizer.name, 1)
			workerID := "embedding-finalization-" + finalizer.name
			jobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
				TeamID: teamID, WorkerID: workerID, Limit: 1, Lease: time.Minute,
			})
			require.NoError(t, err)
			require.Len(t, jobs, 1)

			placementTx := appDB.WithContext(ctx).Begin()
			require.NoError(t, placementTx.Error)
			defer placementTx.Rollback()
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{"SELECT set_config('app.current_team_id', ?, true)", []any{teamID}},
				{"SELECT set_config('app.current_profile_id', ?, true)", []any{ownerID}},
				{"SELECT set_config('app.tx_mode', 'team', true)", nil},
			} {
				require.NoError(t, placementTx.Exec(statement.query, statement.args...).Error)
			}
			require.NoError(t, placementTx.Exec(`SET LOCAL lock_timeout = '2s'`).Error)
			var blockerPID, locked int
			require.NoError(t, placementTx.Raw(`SELECT pg_backend_pid()`).Row().Scan(&blockerPID))
			require.NoError(t, placementTx.Raw(`
				SELECT 1
				FROM search_documents
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
				FOR UPDATE
			`, teamID, document.SearchDocumentID).Row().Scan(&locked))

			finalized := make(chan error, 1)
			go func() { finalized <- finalizer.run(ctx, repo, jobs[0], workerID) }()
			requirePostgresBackendBlockedBy(t, ctx, adminDB, rls, blockerPID)
			updated, err := upsertSearchDocumentInTx(ctx, placementTx, normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
				TeamID: teamID, OwnerProfileID: ownerID, SourceKind: document.SourceKind, SourceID: document.SourceID,
				SourceVersion: document.SourceVersion + 1, ProjectionFormat: document.ProjectionFormat,
				ProjectionGenerationID: document.ProjectionGenerationID, DocumentText: "embedding finalization after " + finalizer.name,
			}), contract, defaultEmbeddingJobMaxAttempts)
			require.NoError(t, err)
			require.NoError(t, placementTx.Commit().Error)
			require.NotNil(t, updated)
			require.NotEmpty(t, updated.QueuedJobID)
			require.ErrorIs(t, <-finalized, ErrEmbeddingLeaseLost)
		})
	}
}

func TestExpiredMaxAttemptCleanupLocksDocumentBeforeJob(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "expired-cleanup-lock-order-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "expired-cleanup-lock-order-owner")
	insertSearchTestContract(t, adminDB, rls, "expired-cleanup-lock-order", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "expired cleanup before", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'processing', attempts = max_attempts, total_attempts = max_attempts,
			    worker_id = 'expired-cleanup-worker',
			    lease_until = clock_timestamp() - interval '1 second',
			    updated_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, document.QueuedJobID).Error
	}))

	placementTx := appDB.WithContext(ctx).Begin()
	require.NoError(t, placementTx.Error)
	defer placementTx.Rollback()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"SELECT set_config('app.current_team_id', ?, true)", []any{teamID}},
		{"SELECT set_config('app.current_profile_id', ?, true)", []any{ownerID}},
		{"SELECT set_config('app.tx_mode', 'team', true)", nil},
	} {
		require.NoError(t, placementTx.Exec(statement.query, statement.args...).Error)
	}
	var locked int
	require.NoError(t, placementTx.Raw(`
		SELECT 1
		FROM search_documents
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		FOR UPDATE
	`, teamID, document.SearchDocumentID).Row().Scan(&locked))

	claimCtx, cancelClaim := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClaim()
	type claimResult struct {
		jobs []EmbeddingJob
		err  error
	}
	claimed := make(chan claimResult, 1)
	go func() {
		jobs, claimErr := repo.ClaimEmbeddingJobs(claimCtx, ClaimEmbeddingJobsInput{
			TeamID: teamID, WorkerID: "next-worker", Limit: 1, Lease: time.Minute,
		})
		claimed <- claimResult{jobs: jobs, err: claimErr}
	}()
	select {
	case result := <-claimed:
		require.NoError(t, result.err)
		require.Empty(t, result.jobs)
	case <-claimCtx.Done():
		t.Fatal("expired max-attempt cleanup locked the job before the document")
	}

	updated, err := upsertSearchDocumentInTx(ctx, placementTx, normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: document.SourceKind, SourceID: document.SourceID,
		SourceVersion: document.SourceVersion + 1, ProjectionFormat: document.ProjectionFormat,
		ProjectionGenerationID: document.ProjectionGenerationID, DocumentText: "expired cleanup after",
	}), contract, defaultEmbeddingJobMaxAttempts)
	require.NoError(t, err)
	require.NoError(t, placementTx.Commit().Error)
	require.NotNil(t, updated)
	require.NotEmpty(t, updated.QueuedJobID)

	var oldStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, document.QueuedJobID).Row().Scan(&oldStatus)
	}))
	require.Equal(t, "stale", oldStatus)
}

func TestEmbeddingReconciliationSuccessfulCanaryLeaseTakeoverResumesBacklog(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-success-takeover-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-success-takeover-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-success-takeover", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	canaryDocument := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "successful takeover canary", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, canaryDocument.SearchDocumentID, "success-takeover-canary-failure")
	backlogDocument := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "successful takeover backlog", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, backlogDocument.SearchDocumentID, "success-takeover-backlog-failure")
	secondBacklogDocument := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "successful takeover second backlog", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, secondBacklogDocument.SearchDocumentID, "success-takeover-second-backlog-failure")
	now := databaseNowForTest(t, adminDB, rls)

	first, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "success-takeover-old", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: first.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: first.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: first.RunID, CanaryJobID: canary.EmbeddingJobID,
		WorkerID: first.WorkerID, LeaseToken: first.LeaseToken, AttemptedAt: now, Lease: time.Minute,
	}))
	require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: canary.TeamID, EmbeddingJobID: canary.EmbeddingJobID,
		WorkerID: EmbeddingReconciliationWorkerIDPrefix + first.RunID, ExpectedAttempts: 1,
		Embedding: []float32{1, 0, 0},
	}))
	require.NoError(t, repo.CompleteEmbeddingReconciliationCanary(ctx, CompleteEmbeddingReconciliationCanaryInput{
		RunID: first.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: first.WorkerID,
		LeaseToken: first.LeaseToken, Succeeded: true, RecoveredCount: 1,
	}))
	firstBatch, err := repo.requeueEmbeddingReconciliationBatch(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: first.RunID, WorkerID: first.WorkerID, LeaseToken: first.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: first.CandidateCutoff, BatchSize: 1, Lease: time.Minute,
	}, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, firstBatch.requeued)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Len(t, convergence.FailureGroups, 1)
	require.Equal(t, "attention_required", convergence.FailureGroups[0].Status)
	require.EqualValues(t, 1, convergence.FailureGroups[0].FailedJobCount)
	require.EqualValues(t, 1, convergence.FailureGroups[0].QueuedJobCount)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_reconciliation_runs
			SET lease_until = clock_timestamp() - interval '1 second'
			WHERE reconciliation_run_id = ?::uuid
		`, first.RunID).Error
	}))

	second, reclaimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: false, WorkerID: "success-takeover-new", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, reclaimed)
	require.Equal(t, "succeeded", second.CanaryOutcome)
	require.EqualValues(t, 1, second.RecoveredCount)
	require.EqualValues(t, 1, second.RequeuedCount)
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)

	requeued, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: second.RunID, WorkerID: second.WorkerID, LeaseToken: second.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: second.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)
	convergence, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Len(t, convergence.FailureGroups, 1)
	require.Equal(t, "recovering", convergence.FailureGroups[0].Status)
	require.Zero(t, convergence.FailureGroups[0].FailedJobCount)
	require.EqualValues(t, 2, convergence.FailureGroups[0].QueuedJobCount)
	requeuedAgain, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: second.RunID, WorkerID: second.WorkerID, LeaseToken: second.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: second.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Zero(t, requeuedAgain)
	require.NoError(t, repo.CompleteEmbeddingReconciliationRun(ctx, CompleteEmbeddingReconciliationRunInput{
		RunID: second.RunID, WorkerID: second.WorkerID, LeaseToken: second.LeaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "succeeded",
		RequeuedCount: second.RequeuedCount + requeued, RecoveredCount: second.RecoveredCount,
	}))

	var canaryStatus, backlogStatus, secondBacklogStatus, runStatus string
	var recoveredCount, requeuedCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, canary.EmbeddingJobID).Scan(&canaryStatus).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, backlogDocument.QueuedJobID).Scan(&backlogStatus).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE embedding_job_id = ?::uuid`, secondBacklogDocument.QueuedJobID).Scan(&secondBacklogStatus).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status, recovered_count, requeued_count
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, second.RunID).Row().Scan(&runStatus, &recoveredCount, &requeuedCount)
	}))
	require.Equal(t, "completed", canaryStatus)
	require.Equal(t, "queued", backlogStatus)
	require.Equal(t, "queued", secondBacklogStatus)
	require.Equal(t, string(domain.EmbeddingReconciliationCompleted), runStatus)
	require.EqualValues(t, 1, recoveredCount)
	require.EqualValues(t, 2, requeuedCount)
}

func TestEmbeddingReconciliationCompletionRejectsExpiredLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-expired-completion-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-expired-completion-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-expired-completion", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "expired reconciliation completion", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, document.SearchDocumentID, "expired-completion-failure")
	cutoff := databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: cutoff, CreateIfMissing: true, WorkerID: "expired-completion-reconciler", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_reconciliation_runs
			SET lease_until = clock_timestamp() - interval '1 second'
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Error
	}))
	require.Error(t, repo.CompleteEmbeddingReconciliationCanary(ctx, CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: document.QueuedJobID, WorkerID: run.WorkerID,
		LeaseToken: run.LeaseToken, Succeeded: true,
	}))
	require.Error(t, repo.CompleteEmbeddingReconciliationRun(ctx, CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: run.WorkerID, LeaseToken: run.LeaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "succeeded",
	}))
	var status, canaryOutcome string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, canary_outcome
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Row().Scan(&status, &canaryOutcome)
	}))
	require.Equal(t, string(domain.EmbeddingReconciliationRunning), status)
	require.Empty(t, canaryOutcome)
}

func TestSearchConvergenceUsesChronologicallyLatestReconciliationRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "reconciliation-latest-run", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	olderRunID := uuid.NewString()
	newerRunID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO embedding_reconciliation_runs (
				reconciliation_run_id, embedding_contract_id, embedding_dimensions,
				local_run_date, status, created_at, updated_at
			) VALUES
				(?::uuid, ?::uuid, 3, DATE '2026-08-11', 'completed',
				 clock_timestamp() - interval '2 hours', clock_timestamp() - interval '2 hours'),
				(?::uuid, ?::uuid, 3, DATE '2026-08-10', 'completed',
				 clock_timestamp() - interval '1 hour', clock_timestamp() - interval '1 hour')
		`, olderRunID, contract.EmbeddingContractID, newerRunID, contract.EmbeddingContractID).Error
	}))

	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.NotNil(t, convergence.LatestRun)
	require.Equal(t, newerRunID, convergence.LatestRun.RunID)
}

func TestCompleteEmbeddingReconciliationRunUsesDatabaseClock(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "reconciliation-completion-clock", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	runID := uuid.NewString()
	leaseToken := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO embedding_reconciliation_runs (
				reconciliation_run_id, embedding_contract_id, embedding_dimensions,
				local_run_date, status, worker_id, lease_token, lease_until, started_at
			) VALUES (
				?::uuid, ?::uuid, 3, DATE '2026-08-10', 'running', ?, ?::uuid,
				clock_timestamp() + interval '1 minute', clock_timestamp()
			)
		`, runID, contract.EmbeddingContractID, "completion-clock-worker", leaseToken).Error
	}))
	require.NoError(t, repo.CompleteEmbeddingReconciliationRun(ctx, CompleteEmbeddingReconciliationRunInput{
		RunID: runID, WorkerID: "completion-clock-worker", LeaseToken: leaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted),
	}))

	var startedAt, completedAt, databaseNow time.Time
	var status string
	var leaseUntil *time.Time
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT started_at, completed_at, status, lease_until, clock_timestamp()
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, runID).Row().Scan(&startedAt, &completedAt, &status, &leaseUntil, &databaseNow)
	}))
	require.False(t, completedAt.Before(startedAt))
	require.False(t, completedAt.After(databaseNow))
	require.Equal(t, string(domain.EmbeddingReconciliationCompleted), status)
	require.Nil(t, leaseUntil)
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

func requirePostgresBackendBlockedBy(
	t *testing.T,
	ctx context.Context,
	adminDB *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	blockerPID int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int64
		err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Raw(`
				SELECT count(*)
				FROM pg_stat_activity AS activity
				WHERE ?::integer = ANY(pg_blocking_pids(activity.pid))
			`, blockerPID).Scan(&waiting).Error
		})
		if err == nil && waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected embedding finalization to wait for the search document lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
