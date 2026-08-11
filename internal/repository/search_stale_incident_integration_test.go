package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchEmbeddingCompletionResolvesStaleIncident(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-stale-incident-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-stale-incident-owner")
	insertSearchTestContract(t, adminDB, rls, "search-stale-incident", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	sourceID := uuid.NewString()
	first, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: sourceID,
		SourceVersion: 1, DocumentText: "old relationship text",
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "worker-stale-incident", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO embedding_failure_incidents (
				team_id, embedding_contract_id, embedding_dimensions, source_kind,
				failure_class, failure_code, status, affected_job_count
			)
			SELECT team_id, embedding_contract_id, embedding_dimensions, source_kind,
			       'permanent', 'unknown_embedding_failure', 'open', 1
			FROM embedding_jobs WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, claimed[0].EmbeddingJobID).Error
	}))
	second, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: sourceID,
		SourceVersion: 2, DocumentText: "new relationship text",
	})
	require.NoError(t, err)
	require.Equal(t, first.SearchDocumentID, second.SearchDocumentID)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE embedding_jobs SET failure_class = 'transient', failure_code = 'provider_timeout' WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, second.QueuedJobID).Error
	}))
	err = repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID, WorkerID: "worker-stale-incident",
		ExpectedAttempts: claimed[0].Attempts, Embedding: []float32{1, 0, 0},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSearchStaleVersion) || errors.Is(err, ErrEmbeddingLeaseLost), "err=%v", err)
	var status string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM embedding_failure_incidents
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND failure_class = 'permanent'
			  AND failure_code = 'unknown_embedding_failure'
		`, teamID).Row().Scan(&status)
	}))
	require.Equal(t, "resolved", status)
}

func TestSearchClaimStaleCleanupBypassesCompatibilityIncidentTriggerLocks(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-stale-trigger-lock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-stale-trigger-lock-owner")
	insertSearchTestContract(t, adminDB, rls, "search-stale-trigger-lock", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "stale trigger lock", 1)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT set_config('app.embedding_job_failure_writer', 'current', true)`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE embedding_jobs
			SET document_version = document_version + 1,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = now(), last_failed_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, document.QueuedJobID).Error
	}))

	locked := make(chan struct{})
	release := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			if err := tx.Exec(`
				SELECT pg_advisory_xact_lock(hashtextextended(
					concat_ws('|', ?::uuid::text, ?::uuid::text, ?::integer::text, ?::text, ?::text, ?::text), 0
				))
			`, teamID, contract.EmbeddingContractID, 3, "evidence", "transient", "provider_timeout").Error; err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timed out acquiring incident advisory lock")
	}

	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	jobs, claimErr := repo.ClaimEmbeddingJobs(claimCtx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "stale-trigger-lock-worker", Limit: 1, Lease: time.Minute,
	})
	cancel()
	close(release)
	require.NoError(t, <-lockErr)
	require.NoError(t, claimErr)
	require.Empty(t, jobs)

	var status string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, document.QueuedJobID).Row().Scan(&status)
	}))
	require.Equal(t, "stale", status)
}

func TestSearchEmbeddingCompletionDoesNotWaitOnUnrelatedIncidentIdentity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-completion-incident-lock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-completion-incident-lock-owner")
	insertSearchTestContract(t, adminDB, rls, "search-completion-incident-lock", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	first := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "completion incident one", 1)
	second := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "completion incident two", 1)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "completion-incident-initial", Limit: 2, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 2)

	jobs := make(map[string]EmbeddingJob, len(claimed))
	for _, job := range claimed {
		jobs[job.EmbeddingJobID] = job
	}
	firstJob := jobs[first.QueuedJobID]
	secondJob := jobs[second.QueuedJobID]
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: firstJob.EmbeddingJobID, WorkerID: "completion-incident-initial",
		ExpectedAttempts: firstJob.Attempts, FailureClass: "transient", FailureCode: "provider_timeout", Terminal: true,
	})
	require.NoError(t, err)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: secondJob.EmbeddingJobID, WorkerID: "completion-incident-initial",
		ExpectedAttempts: secondJob.Attempts, FailureClass: "transient", FailureCode: "provider_server_error", Terminal: true,
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'processing', worker_id = 'completion-incident-worker',
			    lease_until = now() + interval '1 minute', completed_at = NULL
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, firstJob.EmbeddingJobID).Error
	}))

	locked := make(chan struct{})
	release := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			var incidentID string
			if err := tx.Raw(`
				SELECT incident_id::text
				FROM embedding_failure_incidents
				WHERE team_id = ?::uuid AND failure_code = 'provider_server_error' AND status = 'open'
				FOR UPDATE
			`, teamID).Row().Scan(&incidentID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timed out locking unrelated incident")
	}

	completeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	completeErr := repo.CompleteEmbeddingJob(completeCtx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: firstJob.EmbeddingJobID, WorkerID: "completion-incident-worker",
		ExpectedAttempts: firstJob.Attempts, Embedding: []float32{1, 0, 0},
	})
	cancel()
	close(release)
	require.NoError(t, <-lockErr)
	require.NoError(t, completeErr)

	statuses := map[string]string{}
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT failure_code, status
			FROM embedding_failure_incidents
			WHERE team_id = ?::uuid AND failure_code IN ('provider_timeout', 'provider_server_error')
		`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code, status string
			if err := rows.Scan(&code, &status); err != nil {
				return err
			}
			statuses[code] = status
		}
		return rows.Err()
	}))
	require.Equal(t, "resolved", statuses["provider_timeout"])
	require.Equal(t, "open", statuses["provider_server_error"])
}

func TestSearchEmbeddingFailureClassificationTransitionsDoNotDeadlock(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-failure-transition-lock-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-failure-transition-lock-owner")
	insertSearchTestContract(t, adminDB, rls, "search-failure-transition-lock", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	first := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "failure transition one", 1)
	second := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "failure transition two", 1)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "failure-transition-initial", Limit: 2, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 2)

	for index, job := range claimed {
		failureClass, failureCode := "transient", "provider_timeout"
		if index == 1 {
			failureCode = "provider_server_error"
		}
		_, err := repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
			TeamID: teamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: "failure-transition-initial",
			ExpectedAttempts: job.Attempts, FailureClass: failureClass, FailureCode: failureCode, Terminal: true,
		})
		require.NoError(t, err)
	}

	for iteration := 0; iteration < 8; iteration++ {
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE embedding_jobs
				SET status = 'processing', worker_id = 'failure-transition-worker',
				    lease_until = now() + interval '1 minute', completed_at = NULL
				WHERE team_id = ?::uuid AND embedding_job_id IN (?::uuid, ?::uuid)
			`, teamID, first.QueuedJobID, second.QueuedJobID).Error
		}))
		iterationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func(jobID, failureCode string) {
			defer wg.Done()
			_, callErr := repo.FailEmbeddingJob(iterationCtx, FailEmbeddingJobInput{
				TeamID: teamID, EmbeddingJobID: jobID, WorkerID: "failure-transition-worker",
				ExpectedAttempts: 1, FailureClass: "transient", FailureCode: failureCode, Terminal: true,
			})
			errs <- callErr
		}(first.QueuedJobID, "provider_server_error")
		go func(jobID, failureCode string) {
			defer wg.Done()
			_, callErr := repo.FailEmbeddingJob(iterationCtx, FailEmbeddingJobInput{
				TeamID: teamID, EmbeddingJobID: jobID, WorkerID: "failure-transition-worker",
				ExpectedAttempts: 1, FailureClass: "transient", FailureCode: failureCode, Terminal: true,
			})
			errs <- callErr
		}(second.QueuedJobID, "provider_timeout")
		wg.Wait()
		cancel()
		close(errs)
		for callErr := range errs {
			require.NoError(t, callErr)
		}
	}
}

func TestSearchEmbeddingFailureClassificationChangeResolvesPriorIncident(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-reclassified-incident-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-reclassified-incident-owner")
	insertSearchTestContract(t, adminDB, rls, "search-reclassified-incident", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	doc := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "reclassified embedding", 1)

	firstClaim, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "reclassified-worker-one", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: firstClaim[0].EmbeddingJobID,
		WorkerID: "reclassified-worker-one", ExpectedAttempts: firstClaim[0].Attempts,
		FailureClass: "transient", FailureCode: "provider_timeout",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE embedding_jobs SET available_at = now() WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid`, teamID, firstClaim[0].EmbeddingJobID).Error
	}))

	secondClaim, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "reclassified-worker-two", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, secondClaim, 1)
	require.Equal(t, doc.SearchDocumentID, secondClaim[0].SearchDocumentID)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: secondClaim[0].EmbeddingJobID,
		WorkerID: "reclassified-worker-two", ExpectedAttempts: secondClaim[0].Attempts,
		FailureClass: "permanent", FailureCode: "embedding_input_rejected", Terminal: true,
	})
	require.NoError(t, err)

	var oldStatus, oldAffected, newStatus, newAffected string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT old_incident.status, old_incident.affected_job_count::text,
			       new_incident.status, new_incident.affected_job_count::text
			FROM embedding_failure_incidents AS old_incident
			JOIN embedding_failure_incidents AS new_incident
			  ON new_incident.team_id = old_incident.team_id
			WHERE old_incident.team_id = ?::uuid
			  AND old_incident.failure_code = 'provider_timeout'
			  AND old_incident.status = 'resolved'
			  AND new_incident.failure_code = 'embedding_input_rejected'
			  AND new_incident.status = 'open'
		`, teamID).Row().Scan(&oldStatus, &oldAffected, &newStatus, &newAffected)
	}))
	require.Equal(t, "resolved", oldStatus)
	require.Equal(t, "0", oldAffected)
	require.Equal(t, "open", newStatus)
	require.Equal(t, "1", newAffected)
}

func TestSearchEmbeddingIncidentExcludesNeverFailedJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-never-failed-incident-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-never-failed-incident-owner")
	insertSearchTestContract(t, adminDB, rls, "search-never-failed-incident", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	failedDoc := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "failed embedding", 1)
	_ = upsertSearchDocumentForTest(t, repo, teamID, ownerID, "never failed embedding", 1)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "never-failed-worker", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, failedDoc.SearchDocumentID, claimed[0].SearchDocumentID)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID,
		WorkerID: "never-failed-worker", ExpectedAttempts: claimed[0].Attempts,
		FailureClass: "permanent", FailureCode: "unknown_embedding_failure", Terminal: true,
	})
	require.NoError(t, err)

	var affected int64
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = ?::uuid
			  AND failure_class = 'permanent'
			  AND failure_code = 'unknown_embedding_failure'
			  AND status = 'open'
		`, teamID).Row().Scan(&affected)
	}))
	require.EqualValues(t, 1, affected)
}
