package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
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
