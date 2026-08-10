package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestUpsertSearchDocumentRetiresSupersededFailedEmbeddingJob(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "superseded-embedding-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "superseded-embedding-owner")
	insertSearchTestContract(t, adminDB, rls, "superseded-embedding", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)

	sourceID := uuid.NewString()
	first, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "original embedding text", DocumentHash: "sha256:original",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.QueuedJobID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = now(), last_failed_at = now(), completed_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, first.QueuedJobID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', embedding_error = 'provider timeout'
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, first.SearchDocumentID).Error; err != nil {
			return err
		}
		return nil
	}))

	second, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "revised embedding text", DocumentHash: "sha256:revised",
	})
	require.NoError(t, err)
	require.EqualValues(t, first.DocumentVersion+1, second.DocumentVersion)
	require.NotEmpty(t, second.QueuedJobID)

	var oldStatus, replacementStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, first.QueuedJobID).Row().Scan(&oldStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, second.QueuedJobID).Row().Scan(&replacementStatus)
	}))
	require.Equal(t, string(domain.EmbeddingJobStale), oldStatus)
	require.Equal(t, string(domain.EmbeddingJobQueued), replacementStatus)

	third, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 2, ProjectionFormat: 1, DocumentText: "revised embedding text", DocumentHash: "sha256:revised",
	})
	require.NoError(t, err)
	require.Equal(t, second.DocumentVersion, third.DocumentVersion, "a source-only revision keeps the document version")
	require.NotEmpty(t, third.QueuedJobID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, second.QueuedJobID).Row().Scan(&oldStatus)
	}))
	require.Equal(t, string(domain.EmbeddingJobStale), oldStatus,
		"a newer source version must retire the same-version predecessor job")

	convergence, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Zero(t, convergence.Failed, "superseded failed rows must not keep convergence attention required")
	var incidentStatus string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status FROM embedding_failure_incidents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_timeout'
		`, teamID).Row().Scan(&incidentStatus)
	}))
	require.Equal(t, "resolved", incidentStatus)
}

func TestTransactionalPlacementUpsertRetiresSupersededFailedEmbeddingJob(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "transactional-superseded-embedding-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "transactional-superseded-embedding-owner")
	insertSearchTestContract(t, adminDB, rls, "transactional-superseded-embedding", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	sourceID := uuid.NewString()
	first, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "original transactional placement text", DocumentHash: "sha256:transactional-original",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.QueuedJobID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = now(), last_failed_at = now(), completed_at = now(), error = 'provider timeout'
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, first.QueuedJobID).Error
	}))

	var second *SearchDocumentResult
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "revised transactional placement text", DocumentHash: "sha256:transactional-revised",
	})
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var err error
		second, err = upsertSearchDocumentInTx(ctx, tx, input, contract, defaultEmbeddingJobMaxAttempts)
		return err
	}))
	require.NotNil(t, second)
	require.NotEmpty(t, second.QueuedJobID)
	require.EqualValues(t, first.DocumentVersion+1, second.DocumentVersion)

	var oldStatus, replacementStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, first.QueuedJobID).Row().Scan(&oldStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, second.QueuedJobID).Row().Scan(&replacementStatus)
	}))
	require.Equal(t, string(domain.EmbeddingJobStale), oldStatus)
	require.Equal(t, string(domain.EmbeddingJobQueued), replacementStatus)
}

func TestUpsertSearchDocumentPreservesReconciliationCanary(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "reconciliation-canary-source-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "reconciliation-canary-source-owner")
	insertSearchTestContract(t, adminDB, rls, "reconciliation-canary-source", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)

	sourceID := uuid.NewString()
	first, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "canary source version one", DocumentHash: "sha256:canary-one",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.QueuedJobID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'processing', worker_id = ?, lease_until = now() + interval '5 minutes',
			    attempts = 1, total_attempts = 1
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, EmbeddingReconciliationWorkerIDPrefix+"run-1", teamID, first.QueuedJobID).Error
	}))

	second, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 2, ProjectionFormat: 1, DocumentText: "canary source version two", DocumentHash: "sha256:canary-two",
	})
	require.NoError(t, err)
	require.NotEmpty(t, second.QueuedJobID)

	var status, workerID string
	var leasePresent bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, worker_id, lease_until IS NOT NULL
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, first.QueuedJobID).Row().Scan(&status, &workerID, &leasePresent)
	}))
	assert.Equal(t, string(domain.EmbeddingJobProcessing), status)
	assert.Equal(t, EmbeddingReconciliationWorkerIDPrefix+"run-1", workerID)
	assert.True(t, leasePresent)
}
