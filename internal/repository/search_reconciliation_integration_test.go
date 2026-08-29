package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCheckSearchConvergenceUsesCanonicalDocumentState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "convergence-health-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "convergence-health-owner")
	insertSearchTestContract(t, adminDB, rls, "convergence-health", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	_, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{EmbeddingDimensions: 4})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	require.NoError(t, repo.CheckSearchConvergence(ctx))
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "convergence health backlog",
	})
	require.NoError(t, err)
	require.NotEmpty(t, document.SearchDocumentID)
	require.Equal(t, "pending", document.SearchState)
	require.NotEmpty(t, document.QueuedJobID)
	err = repo.CheckSearchConvergence(ctx)
	require.ErrorIs(t, err, ErrSearchConvergenceAttentionRequired)
	require.ErrorContains(t, err, "attention_required")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'stale', completed_at = now(), updated_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, document.QueuedJobID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[1,0,0]'::vector,
			    embedding_updated_at = now(), embedding_error = '', updated_at = now()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))
	require.NoError(t, repo.CheckSearchConvergence(ctx))
}

func TestReserveSearchRepairRunDoesNotConsumeNoWorkDate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "search-repair-no-work", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        time.Now().UTC(),
		CreateIfMissing:     true,
		WorkerID:            "search-repair-no-work-worker",
		Lease:               time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, run)
}

func TestReserveEmbeddingReconciliationRunWaitsForRecoverableCandidate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-reserve-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-reserve-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-reserve", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Minute)

	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "reserve-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, run, "a no-work scheduler pass must not consume the local reconciliation date")

	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "reconciliation reserve candidate", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?, error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))

	run, claimed, err = repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "reserve-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
}

func TestReserveEmbeddingReconciliationRunDoesNotReclaimFailedRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-failed-run-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-failed-run-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-failed-run", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "failed run candidate", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, document.SearchDocumentID, "failed-run-writer")
	now := databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "failed-run-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_reconciliation_runs
			SET status = 'failed', completed_at = now(), lease_until = now() - interval '1 second'
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Error
	}))

	reloaded, reclaimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: now, CreateIfMissing: false, WorkerID: "failed-run-worker-2", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, reclaimed)
	require.Equal(t, string(domain.EmbeddingReconciliationFailed), reloaded.Status)
}

func TestEmbeddingReconciliationCutoffAndLeasesUseDatabaseClock(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-database-clock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-database-clock-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-database-clock", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "database clock candidate", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		failedAt := time.Now().UTC().Add(-time.Minute)
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?, error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, failedAt, failedAt, failedAt, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))
	databaseBefore := databaseNowForTest(t, adminDB, rls)
	sharedTime, err := repo.GetEmbeddingReconciliationTime(ctx)
	require.NoError(t, err)
	databaseAfter := databaseNowForTest(t, adminDB, rls)
	require.WithinRange(t, sharedTime, databaseBefore, databaseAfter)
	future := time.Now().UTC().Add(24 * time.Hour)
	databaseBefore = databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: future, CreateIfMissing: true, WorkerID: "database-clock-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	databaseAfter = databaseNowForTest(t, adminDB, rls)
	require.False(t, run.CandidateCutoff.Before(databaseBefore))
	require.False(t, run.CandidateCutoff.After(databaseAfter))
	var leaseSeconds float64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXTRACT(EPOCH FROM (lease_until - clock_timestamp()))
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Scan(&leaseSeconds).Error
	}))
	require.Greater(t, leaseSeconds, 30.0)
	require.Less(t, leaseSeconds, 90.0)
	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: 3, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID,
		WorkerID: "database-clock-worker", LeaseToken: run.LeaseToken,
		AttemptedAt: future, Lease: time.Minute,
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXTRACT(EPOCH FROM (lease_until - clock_timestamp()))
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Scan(&leaseSeconds).Error
	}))
	require.Greater(t, leaseSeconds, 30.0)
	require.Less(t, leaseSeconds, 90.0)
}

func TestEmbeddingReconciliationRunFencesOneCanaryAndRequeuesPreCutoffBacklog(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-team")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "reconciliation-other-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	first := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "reconciliation canary", 1)
	second := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "reconciliation backlog", 1)
	cutoff := databaseNowForTest(t, adminDB, rls)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = 20, total_attempts = 20,
			    failure_class = 'provider_action_required',
			    failure_code = 'provider_quota_exhausted',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?,
			    error = 'quota failure'
			WHERE team_id = ?::uuid
			  AND search_document_id IN (?::uuid, ?::uuid)
		`, cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), teamID, first.SearchDocumentID, second.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE search_documents SET search_state = 'failed', embedding_error = 'quota failure' WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)`, teamID, first.SearchDocumentID, second.SearchDocumentID).Error
	}))

	workerID := "reconciliation-test-worker"
	reserveInput := ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        cutoff,
		CreateIfMissing:     true,
		WorkerID:            workerID,
		Lease:               time.Minute,
	}
	type reserveResult struct {
		run     *EmbeddingReconciliationRun
		claimed bool
		err     error
	}
	results := make(chan reserveResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		run, claimed, reserveErr := repo.ReserveEmbeddingReconciliationRun(ctx, reserveInput)
		results <- reserveResult{run: run, claimed: claimed, err: reserveErr}
	}()
	go func() {
		defer wg.Done()
		run, claimed, reserveErr := repo.ReserveEmbeddingReconciliationRun(ctx, reserveInput)
		results <- reserveResult{run: run, claimed: claimed, err: reserveErr}
	}()
	wg.Wait()
	firstResult, secondResult := <-results, <-results
	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	require.NotNil(t, firstResult.run)
	require.NotNil(t, secondResult.run)
	require.NotEqual(t, firstResult.claimed, secondResult.claimed, "only one competing reconciler may hold the run lease")
	run := firstResult.run
	if !firstResult.claimed {
		run = secondResult.run
	}

	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: cutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.Error(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: otherTeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: workerID,
		LeaseToken: run.LeaseToken, AttemptedAt: cutoff,
	}), "a canary job must not be claimable through another team scope")
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: workerID,
		LeaseToken: run.LeaseToken, AttemptedAt: cutoff,
	}))
	var canaryError string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, canary.TeamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT error
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, canary.TeamID, canary.EmbeddingJobID).Row().Scan(&canaryError)
	}))
	require.Empty(t, canaryError, "a retried canary must not retain its prior failure")

	canaryAgain, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: cutoff,
	})
	require.NoError(t, err)
	require.Nil(t, canaryAgain, "a pre-call marker must prevent a same-day second canary")

	secondRunCount, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: workerID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: cutoff, BatchSize: 1,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, secondRunCount, "the canary remains processing; only the other failed row is released")

	backlogDocumentID := second.SearchDocumentID
	if canary.SearchDocumentID == backlogDocumentID {
		backlogDocumentID = first.SearchDocumentID
	}
	var status string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM embedding_jobs WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, backlogDocumentID).Scan(&status).Error
	}))
	require.Equal(t, "queued", status)
}

func TestEmbeddingReconciliationCanaryResetSkipsPermanentInputFailure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-input-skip-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-input-skip-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-input-skip", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	first := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "input rejected canary", 1)
	second := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "healthy canary", 1)
	now := time.Now().UTC().Truncate(time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?, error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)
		`, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), teamID, first.SearchDocumentID, second.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)
		`, teamID, first.SearchDocumentID, second.SearchDocumentID).Error
	}))

	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "input-skip-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: 3, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID,
		WorkerID: "input-skip-worker", LeaseToken: run.LeaseToken, AttemptedAt: now,
	}))
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: canary.TeamID, EmbeddingJobID: canary.EmbeddingJobID,
		WorkerID:         EmbeddingReconciliationWorkerIDPrefix + run.RunID,
		ExpectedAttempts: 1, Error: "daily embedding canary rejected the input",
		FailureClass: string(domain.EmbeddingFailurePermanent), FailureCode: string(domain.EmbeddingFailureInputRejected), Terminal: true,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CompleteEmbeddingReconciliationCanary(ctx, CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: "input-skip-worker",
		LeaseToken: run.LeaseToken, FailureClass: string(domain.EmbeddingFailurePermanent), FailureCode: string(domain.EmbeddingFailureInputRejected),
	}))
	require.NoError(t, repo.ResetEmbeddingReconciliationCanary(ctx, ResetEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: "input-skip-worker", LeaseToken: run.LeaseToken,
	}))

	next, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: 3, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, canary.EmbeddingJobID, next.EmbeddingJobID)
	require.Contains(t, []string{first.SearchDocumentID, second.SearchDocumentID}, next.SearchDocumentID)
}

func TestEmbeddingReconciliationRenewsLeaseAndTracksDerivedFailureGroupRecovery(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-renew-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-renew-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-renew", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	first, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "first recoverable failure",
	})
	require.NoError(t, err)
	second, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: uuid.NewString(),
		SourceVersion: 1, DocumentText: "second recoverable failure",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?
			WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)
		`, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), teamID, first.SearchDocumentID, second.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'embedding provider timed out'
			WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)
		`, teamID, first.SearchDocumentID, second.SearchDocumentID).Error
	}))

	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: now, CreateIfMissing: true, WorkerID: "renew-worker", Lease: 30 * time.Second,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: 3, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: "renew-worker",
		LeaseToken: run.LeaseToken, AttemptedAt: now, Lease: 30 * time.Second,
	}))

	requeued, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: "renew-worker", LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)

	var renewedSeconds float64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXTRACT(EPOCH FROM (lease_until - clock_timestamp()))
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
		`, run.RunID).Scan(&renewedSeconds).Error
	}))
	require.Greater(t, renewedSeconds, 240.0)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Len(t, convergence.FailureGroups, 1)
	require.Equal(t, "recovering", convergence.FailureGroups[0].Status)
	require.EqualValues(t, 2, convergence.FailureGroups[0].AffectedJobCount)
	require.EqualValues(t, 1, convergence.FailureGroups[0].QueuedJobCount)
	require.EqualValues(t, 1, convergence.FailureGroups[0].ProcessingJobCount)

	require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: canary.EmbeddingJobID,
		WorkerID:         EmbeddingReconciliationWorkerIDPrefix + run.RunID,
		ExpectedAttempts: 1, Embedding: []float32{1, 0, 0},
	}))
	convergence, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Len(t, convergence.FailureGroups, 1)
	require.Equal(t, "recovering", convergence.FailureGroups[0].Status)
	require.EqualValues(t, 1, convergence.FailureGroups[0].AffectedJobCount)

	claimedJobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{TeamID: teamID, WorkerID: "normal-worker", Limit: 1, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, claimedJobs, 1)
	require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimedJobs[0].EmbeddingJobID,
		WorkerID: "normal-worker", ExpectedAttempts: claimedJobs[0].Attempts,
		Embedding: []float32{0, 1, 0},
	}))
	convergence, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "converged", convergence.Status)
	require.Empty(t, convergence.FailureGroups)
}

func TestSearchReadinessIgnoresAsynchronousEmbeddingFailures(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "readiness-convergence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "readiness-convergence-owner")
	insertSearchTestContract(t, adminDB, rls, "readiness-convergence", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "lexical text remains usable", 1)
	upsertSearchDocumentForTest(t, repo, teamID, ownerID, "unrelated queued work", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'provider_action_required', failure_code = 'provider_quota_exhausted',
			    completed_at = now(), first_failed_at = now(), last_failed_at = now()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE search_documents SET search_state = 'failed', embedding_error = 'quota failure' WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return nil
	}))
	readiness, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.True(t, readiness.Ready, "readiness reasons: %+v", readiness.Reasons)
	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "attention_required", convergence.Status)
	require.EqualValues(t, 1, convergence.Queued)
	require.EqualValues(t, 1, convergence.Failed)
	require.Len(t, convergence.Failures, 1)
	require.Equal(t, "provider_quota_exhausted", convergence.Failures[0].FailureCode)
	require.Len(t, convergence.FailureGroups, 1)
	require.Equal(t, teamID, convergence.FailureGroups[0].TeamID)
	require.Equal(t, "provider_quota_exhausted", convergence.FailureGroups[0].FailureCode)
	require.EqualValues(t, 1, convergence.FailureGroups[0].FailedJobCount)
}

func TestSearchConvergenceBoundsDerivedFailureGroups(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "convergence-failure-group-bound", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	teams := []struct {
		teamID  string
		ownerID string
	}{
		{teamID: createLedgerTeam(t, adminDB, rls, "convergence-failure-group-bound-a")},
		{teamID: createLedgerTeam(t, adminDB, rls, "convergence-failure-group-bound-b")},
		{teamID: createLedgerTeam(t, adminDB, rls, "convergence-failure-group-bound-c")},
		{teamID: createLedgerTeam(t, adminDB, rls, "convergence-failure-group-bound-d")},
	}
	for index := range teams {
		teams[index].ownerID = createLedgerProfile(t, adminDB, rls, teams[index].teamID, "convergence-failure-group-owner")
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teams[index].teamID, teams[index].ownerID, func(tx *gorm.DB) error {
			return seedTeamPredicateDefinitions(ctx, tx, teams[index].teamID)
		}))
	}
	failureContracts := []struct {
		class string
		code  string
	}{
		{"transient", "provider_rate_limited"},
		{"transient", "provider_timeout"},
		{"transient", "provider_network_error"},
		{"transient", "provider_server_error"},
		{"provider_action_required", "provider_quota_exhausted"},
		{"provider_action_required", "provider_authentication_failed"},
		{"provider_action_required", "provider_permission_denied"},
		{"provider_action_required", "provider_contract_rejected"},
		{"provider_action_required", "provider_response_invalid"},
		{"permanent", "embedding_input_rejected"},
		{"permanent", "embedding_contract_mismatch"},
		{"permanent", "unknown_embedding_failure"},
	}
	sourceKinds := []string{"evidence", "relationship", "entity"}
	groupIndex := 0
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, team := range teams {
			for _, failure := range failureContracts {
				for _, sourceKind := range sourceKinds {
					documentID := uuid.NewString()
					sourceID := uuid.NewString()
					jobID := uuid.NewString()
					status := "queued"
					searchState := "pending"
					attempts := 1
					failedAt := time.Now().UTC().Add(time.Minute)
					var completedAt any
					if groupIndex == 0 {
						status = "failed"
						searchState = "failed"
						attempts = 20
						failedAt = time.Now().UTC().Add(-24 * time.Hour)
						completedAt = failedAt
					}
					if err := tx.Exec(`
						INSERT INTO search_documents (
							team_id, search_document_id, owner_profile_id, source_kind, source_id,
							source_version, document_version, embedding_contract_id,
							embedding_dimensions, search_state, document_text, document_hash,
							embedding_error
						) VALUES (
							?::uuid, ?::uuid, ?::uuid, ?, ?::uuid,
							1, 1, ?::uuid, ?, ?, ?, ?, 'bounded failure'
						)
					`, team.teamID, documentID, team.ownerID, sourceKind, sourceID,
						contract.EmbeddingContractID, contract.EmbeddingDimensions, searchState,
						"bounded failure group", "sha256:"+strings.ReplaceAll(documentID, "-", "")).Error; err != nil {
						return err
					}
					if err := tx.Exec(`
						INSERT INTO embedding_jobs (
							team_id, embedding_job_id, search_document_id, owner_profile_id,
							source_kind, source_id, source_version, document_version,
							embedding_contract_id, embedding_dimensions, status, attempts,
							total_attempts, max_attempts, error, completed_at,
							failure_class, failure_code, first_failed_at, last_failed_at
						) VALUES (
							?::uuid, ?::uuid, ?::uuid, ?::uuid,
							?, ?::uuid, 1, 1, ?::uuid, ?, ?, ?, ?, 20,
							'bounded failure', ?, ?, ?, ?, ?
						)
					`, team.teamID, jobID, documentID, team.ownerID,
						sourceKind, sourceID, contract.EmbeddingContractID, contract.EmbeddingDimensions,
						status, attempts, attempts, completedAt, failure.class, failure.code, failedAt, failedAt).Error; err != nil {
						return err
					}
					groupIndex++
				}
			}
		}
		return nil
	}))

	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 144, convergence.FailureGroupCount)
	require.Len(t, convergence.FailureGroups, searchConvergenceFailureGroupLimit)
	require.True(t, convergence.FailureGroupsTruncated)
	for _, group := range convergence.FailureGroups {
		require.Equal(t, "recovering", group.Status)
		require.GreaterOrEqual(t, group.Age, time.Duration(0))
	}
	require.Equal(t, "attention_required", convergence.Status)
}

func TestSearchConvergenceExcludesDeletedTeams(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "convergence-deleted-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "convergence-deleted-owner")
	insertSearchTestContract(t, adminDB, rls, "convergence-deleted", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "deleted team failure history", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    completed_at = now(), first_failed_at = now(), last_failed_at = now()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'provider timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))

	before, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "attention_required", before.Status)
	require.EqualValues(t, 1, before.Failed)
	require.Len(t, before.FailureGroups, 1)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: databaseNowForTest(t, adminDB, rls), CreateIfMissing: true,
		WorkerID: "deleted-team-reconciler", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)

	profileRepo := NewTeamRepository(appDB, rls)
	require.NoError(t, profileRepo.SoftDelete(ctx, uuid.MustParse(teamID)))
	requeued, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: run.WorkerID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		CandidateCutoff: run.CandidateCutoff, BatchSize: 500, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Zero(t, requeued)

	after, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "converged", after.Status)
	require.Zero(t, after.Failed)
	require.Empty(t, after.Failures)
	require.Empty(t, after.FailureGroups)

	var failedJobs int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM embedding_jobs WHERE team_id = ?::uuid AND status = 'failed'`, teamID).Scan(&failedJobs).Error
	}))
	require.EqualValues(t, 1, failedJobs)
}

func TestSearchReadinessAcceptsFailedRelationshipProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "readiness-failed-relationship-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "readiness-failed-relationship-owner")
	insertSearchTestContract(t, adminDB, rls, "readiness-failed-relationship", 3, "exact", "")
	searchRepo := NewSearchRepository(appDB, rls)
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Readiness owner")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Readiness project")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"readiness failed relationship", "Readiness owner works on Readiness project.")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "readiness:failed-relationship",
			SpanStart:      0,
			SpanEnd:        len("Readiness owner works on Readiness project."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	document, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       decision.Relationship.RelationshipID,
		SourceVersion:  int64(decision.Relationship.Version),
		DocumentText:   "relationship\nsubject: Readiness owner\npredicate: works_on\nobject: Readiness project",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", document.SearchState)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'provider_action_required', failure_code = 'provider_quota_exhausted',
			    completed_at = now(), first_failed_at = now(), last_failed_at = now()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'quota failure'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))

	readiness, err := searchRepo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.True(t, readiness.Ready, "readiness reasons: %+v", readiness.Reasons)
}

func TestEmbeddingReconciliationExcludesPermanentFailuresAndFencesExpiredLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-fence", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	permanent := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "permanent input", 1)
	recoverable := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "recoverable quota", 1)
	cutoff := time.Date(2026, 8, 10, 4, 30, 0, 0, time.FixedZone("PDT", -7*60*60))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = 4, total_attempts = 4,
			    failure_class = CASE WHEN search_document_id = ?::uuid THEN 'permanent' ELSE 'provider_action_required' END,
			    failure_code = CASE WHEN search_document_id = ?::uuid THEN 'embedding_input_rejected' ELSE 'provider_quota_exhausted' END,
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?
			WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)
		`, permanent.SearchDocumentID, permanent.SearchDocumentID, cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), cutoff.Add(-time.Minute), teamID, permanent.SearchDocumentID, recoverable.SearchDocumentID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE search_documents SET search_state = 'failed' WHERE team_id = ?::uuid AND search_document_id IN (?::uuid, ?::uuid)`, teamID, permanent.SearchDocumentID, recoverable.SearchDocumentID).Error
	}))

	first, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: cutoff, CreateIfMissing: true, WorkerID: "worker-old", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_reconciliation_runs
			SET lease_until = clock_timestamp() - interval '1 second'
			WHERE reconciliation_run_id = ?::uuid
		`, first.RunID).Error
	}))
	second, reclaimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: cutoff, CreateIfMissing: false, WorkerID: "worker-new", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, reclaimed, "an expired run lease must be reclaimable")
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)

	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: second.RunID, EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3, CandidateCutoff: cutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.Equal(t, recoverable.SearchDocumentID, canary.SearchDocumentID, "permanent data failures must stay blocked")
	require.Error(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: second.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: "worker-old", LeaseToken: first.LeaseToken, AttemptedAt: cutoff,
	}), "stale worker must not mark the canary")
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: second.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: "worker-new", LeaseToken: second.LeaseToken, AttemptedAt: cutoff.Add(2 * time.Minute),
	}))
	require.Error(t, func() error {
		_, err := repo.RequeueEmbeddingReconciliationJobs(ctx, RequeueEmbeddingReconciliationJobsInput{
			RunID: second.RunID, WorkerID: "worker-old", LeaseToken: first.LeaseToken,
			EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3, CandidateCutoff: cutoff, BatchSize: 500,
		})
		return err
	}(), "stale worker must not release backlog")

	var permanentStatus, recoverableStatus string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM embedding_jobs WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, permanent.SearchDocumentID).Row().Scan(&permanentStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM embedding_jobs WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, recoverable.SearchDocumentID).Row().Scan(&recoverableStatus)
	}))
	require.Equal(t, "failed", permanentStatus)
	require.Equal(t, "processing", recoverableStatus)
}

func TestEmbeddingReconciliationReservePreservesLocalRunDate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-local-date-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-local-date-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-local-date", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	local := time.Date(2026, 8, 10, 23, 30, 0, 0, time.FixedZone("PDT", -7*60*60))
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "reconciliation local date candidate", 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		failedAt := local.UTC().Add(-time.Minute)
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = ?, last_failed_at = ?, completed_at = ?, error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, failedAt, failedAt, failedAt, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error
	}))
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: 3,
		LocalRunDate: local, CreateIfMissing: true, WorkerID: "local-date-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	var storedDate string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT local_run_date::text FROM embedding_reconciliation_runs WHERE reconciliation_run_id = ?::uuid`, run.RunID).Scan(&storedDate).Error
	}))
	require.Equal(t, "2026-08-10", storedDate)
}

func TestFailedEmbeddingRemainsLexicalRecallEligibleButNotVectorEligible(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-lexical-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-lexical-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-lexical", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "lexical recovery remains available", DocumentHash: "sha256:lexical-recovery",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'provider_action_required', failure_code = 'provider_quota_exhausted',
			    first_failed_at = now(), last_failed_at = now(), completed_at = now()
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE search_documents SET search_state = 'failed', embedding_error = 'embedding provider quota exhausted' WHERE team_id = ?::uuid AND search_document_id = ?::uuid`, teamID, document.SearchDocumentID).Error
	}))

	hits, err := repo.SearchFullText(ctx, FullTextSearchInput{TeamID: teamID, Query: "lexical recovery", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, document.SearchDocumentID, hits[0].SearchDocumentID)
	require.Equal(t, "failed", hits[0].SearchState)

	vectorHits, err := repo.SearchExactVector(ctx, ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{1, 0, 0}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, vectorHits)
}

func TestEmbeddingReconciliationFailureValidationIsClosed(t *testing.T) {
	validID := uuid.NewString()
	valid := CompleteEmbeddingReconciliationCanaryInput{
		RunID: validID, CanaryJobID: uuid.NewString(), WorkerID: "worker", LeaseToken: uuid.NewString(),
		FailureClass: string(domain.EmbeddingFailureTransient), FailureCode: string(domain.EmbeddingFailureProviderTimeout),
	}
	require.NoError(t, validateCompleteEmbeddingReconciliationCanaryInput(valid))
	invalid := valid
	invalid.FailureCode = string(domain.EmbeddingFailureProviderQuotaExhausted)
	require.Error(t, validateCompleteEmbeddingReconciliationCanaryInput(invalid))
	invalidRun := CompleteEmbeddingReconciliationRunInput{
		RunID: validID, WorkerID: "worker", LeaseToken: uuid.NewString(), Status: string(domain.EmbeddingReconciliationDeferred),
		FailureClass: string(domain.EmbeddingFailurePermanent), FailureCode: string(domain.EmbeddingFailureProviderTimeout),
	}
	require.Error(t, validateCompleteEmbeddingReconciliationRunInput(invalidRun))
	require.Equal(t, "reconciliation operation failed", boundedReconciliationError("provider failed", "provider-controlled-code"))
}
