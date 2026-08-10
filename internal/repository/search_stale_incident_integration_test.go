package repository

import (
	"context"
	"errors"
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
