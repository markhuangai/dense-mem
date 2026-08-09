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
			       failure_class, failure_code, 'open', 1
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
	require.True(t, errors.Is(err, ErrSearchStaleVersion), "err=%v", err)
	var status string
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM embedding_failure_incidents WHERE team_id = ?::uuid AND source_kind = 'relationship'`, teamID).Row().Scan(&status)
	}))
	require.Equal(t, "resolved", status)
}
