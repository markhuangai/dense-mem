package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const v2SearchTestHNSWIndexName = "v2_search_documents_test_3_halfvec_hnsw_idx"

var v2SearchTestContractSequence atomic.Int32

func TestV2SearchRepositoryFailsClosedWithoutDependencies(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.NewString()

	_, err := (&V2SearchRepositoryImpl{}).ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker",
		Lease:    time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is required")

	_, err = (&V2SearchRepositoryImpl{db: &gorm.DB{}}).ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker",
		Lease:    time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rls helper is required")
}

func TestV2SearchRepositoryPersistsConfiguredEmbeddingJobMaxAttempts(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-attempt-policy-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-attempt-policy-owner")
	insertV2SearchTestContract(t, adminDB, rls, "search-attempt-policy", 3, "exact", "")

	customRepo := NewV2SearchRepositoryWithEmbeddingJobMaxAttempts(appDB, rls, 37)
	custom := upsertV2SearchDocumentForTest(t, customRepo, teamID, ownerID, "custom retry policy", 1)
	defaultRepo := NewV2SearchRepository(appDB, rls)
	standard := upsertV2SearchDocumentForTest(t, defaultRepo, teamID, ownerID, "default retry policy", 1)

	attemptsByDocument := map[string]int{}
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT search_document_id::text, max_attempts
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND search_document_id IN (?::uuid, ?::uuid)
		`, teamID, custom.SearchDocumentID, standard.SearchDocumentID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var documentID string
			var maxAttempts int
			if err := rows.Scan(&documentID, &maxAttempts); err != nil {
				return err
			}
			attemptsByDocument[documentID] = maxAttempts
		}
		return rows.Err()
	}))
	assert.Equal(t, 37, attemptsByDocument[custom.SearchDocumentID])
	assert.Equal(t, defaultV2EmbeddingJobMaxAttempts, attemptsByDocument[standard.SearchDocumentID])
}

func TestV2SearchDocumentsFTSAndExactVectorAreTeamScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "search-team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "search-owner-a")
	teamC := createV2LedgerTeam(t, adminDB, rls, "search-team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "search-owner-c")
	insertV2SearchTestContract(t, adminDB, rls, "search-exact", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	docA := upsertV2SearchDocumentForTest(t, repo, teamA, ownerA, "postgres pgvector durable memory", 1)
	docB := upsertV2SearchDocumentForTest(t, repo, teamA, ownerA, "pgvector ranking contract", 1)
	docC := upsertV2SearchDocumentForTest(t, repo, teamC, ownerC, "postgres pgvector other team", 1)
	completeV2SearchJobsForTest(t, repo, teamA, map[string][]float32{
		docA.SearchDocumentID: {1, 0, 0},
		docB.SearchDocumentID: {0, 1, 0},
	})
	completeV2SearchJobsForTest(t, repo, teamC, map[string][]float32{
		docC.SearchDocumentID: {1, 0, 0},
	})

	vectorHits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamA,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          5,
	})
	require.NoError(t, err)
	require.Len(t, vectorHits, 2)
	assert.Equal(t, docA.SearchDocumentID, vectorHits[0].SearchDocumentID)
	assert.Equal(t, teamA, vectorHits[0].TeamID)
	assert.NotEqual(t, docC.SearchDocumentID, vectorHits[0].SearchDocumentID)
	assert.Less(t, vectorHits[0].Distance, vectorHits[1].Distance)

	textHits, err := repo.SearchFullText(ctx, V2FullTextSearchInput{
		TeamID:     teamA,
		Query:      "pgvector",
		SourceKind: "evidence",
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, textHits, 2)
	for _, hit := range textHits {
		assert.Equal(t, teamA, hit.TeamID)
		assert.NotEqual(t, docC.SearchDocumentID, hit.SearchDocumentID)
	}
}

func TestV2SearchExactVectorUsesCosineDistance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-metric-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-metric-owner")
	repo := NewV2SearchRepository(appDB, rls)

	insertV2SearchTestContract(t, adminDB, rls, "search-cosine", 3, "exact", "")
	expected := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "cosine nearest vector", 1)
	other := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "cosine farther vector", 1)
	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		expected.SearchDocumentID: {1, 0, 0},
		other.SearchDocumentID:    {0, 1, 0},
	})

	hits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          2,
	})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, expected.SearchDocumentID, hits[0].SearchDocumentID)
	assert.Less(t, hits[0].Distance, hits[1].Distance)
}

func TestV2SearchEmbeddingCompletionRejectsStaleJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-stale-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-stale-owner")
	insertV2SearchTestContract(t, adminDB, rls, "search-stale", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)
	sourceID := uuid.NewString()

	first, err := repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       sourceID,
		SourceVersion:  1,
		DocumentText:   "old relationship text",
	})
	require.NoError(t, err)

	claimed, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-stale",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, first.SearchDocumentID, claimed[0].SearchDocumentID)

	second, err := repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "relationship",
		SourceID:       sourceID,
		SourceVersion:  2,
		DocumentText:   "new relationship text",
	})
	require.NoError(t, err)
	require.Equal(t, first.SearchDocumentID, second.SearchDocumentID)
	require.Equal(t, int64(2), second.DocumentVersion)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   claimed[0].EmbeddingJobID,
		WorkerID:         "worker-stale",
		ExpectedAttempts: claimed[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchStaleVersion), "err=%v", err)

	var jobStatus string
	var jobCompleted bool
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, completed_at IS NOT NULL
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, claimed[0].EmbeddingJobID).Row().Scan(&jobStatus, &jobCompleted)
	})
	require.NoError(t, err)
	assert.Equal(t, "stale", jobStatus)
	assert.True(t, jobCompleted)

	hits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.NoError(t, err)
	assert.Empty(t, hits, "stale completion must not make the newer document vector-current")

	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		second.SearchDocumentID: {1, 0, 0},
	})
	hits, err = repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, int64(2), hits[0].SourceVersion)
	assert.Equal(t, int64(2), hits[0].DocumentVersion)
}

func TestV2SearchUpsertRejectsStaleSourceVersion(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-upsert-stale-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-upsert-stale-owner")
	contractID := insertV2SearchTestContract(t, adminDB, rls, "search-upsert-stale", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)
	sourceID := uuid.NewString()

	current, err := repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       sourceID,
		SourceVersion:  2,
		DocumentText:   "authoritative version two",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), current.SourceVersion)

	_, err = repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       sourceID,
		SourceVersion:  1,
		DocumentText:   "delayed version one",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchStaleVersion), "err=%v", err)

	var sourceVersion int64
	var documentVersion int64
	var documentText string
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT source_version, document_version, document_text
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
		`, teamID, sourceID, contractID).Row().Scan(&sourceVersion, &documentVersion, &documentText)
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), sourceVersion)
	assert.Equal(t, int64(1), documentVersion)
	assert.Equal(t, "authoritative version two", documentText)
}

func TestV2SearchClaimEmbeddingJobsReclaimsExpiredLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-reclaim-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-reclaim-owner")
	insertV2SearchTestContract(t, adminDB, rls, "search-reclaim", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "lease reclaim text", 1)
	firstClaim, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-one",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)
	require.Equal(t, doc.SearchDocumentID, firstClaim[0].SearchDocumentID)
	require.Equal(t, 1, firstClaim[0].Attempts)

	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, firstClaim[0].EmbeddingJobID).Error
	})
	require.NoError(t, err)

	secondClaim, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-two",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, secondClaim, 1)
	assert.Equal(t, firstClaim[0].EmbeddingJobID, secondClaim[0].EmbeddingJobID)
	assert.Equal(t, 2, secondClaim[0].Attempts)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   firstClaim[0].EmbeddingJobID,
		WorkerID:         "worker-one",
		ExpectedAttempts: firstClaim[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2EmbeddingLeaseLost), "err=%v", err)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   secondClaim[0].EmbeddingJobID,
		WorkerID:         "worker-two",
		ExpectedAttempts: secondClaim[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.NoError(t, err)
}

func TestV2SearchCompletionRejectsExpiredLeaseBeforeReclaim(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-expired-complete-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-expired-complete-owner")
	insertV2SearchTestContract(t, adminDB, rls, "search-expired-complete", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "lease expired before completion", 1)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-expired",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, doc.SearchDocumentID, claimed[0].SearchDocumentID)

	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, claimed[0].EmbeddingJobID).Error
	})
	require.NoError(t, err)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   claimed[0].EmbeddingJobID,
		WorkerID:         "worker-expired",
		ExpectedAttempts: claimed[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2EmbeddingLeaseLost), "err=%v", err)

	var jobStatus string
	var documentState string
	var embeddingPresent bool
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT job.status, document.search_state, document.embedding IS NOT NULL
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id
			 AND document.search_document_id = job.search_document_id
			WHERE job.team_id = ?::uuid
			  AND job.embedding_job_id = ?::uuid
		`, teamID, claimed[0].EmbeddingJobID).Row().Scan(&jobStatus, &documentState, &embeddingPresent)
	})
	require.NoError(t, err)
	assert.Equal(t, "processing", jobStatus)
	assert.Equal(t, "pending", documentState)
	assert.False(t, embeddingPresent)

	reclaimed, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-reclaim-after-expiry",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, claimed[0].EmbeddingJobID, reclaimed[0].EmbeddingJobID)
}

func TestV2SearchClaimEmbeddingJobsFailsExpiredExhaustedJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-exhausted-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-exhausted-owner")
	insertV2SearchTestContract(t, adminDB, rls, "search-exhausted", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "exhausted embedding job text", 1)
	firstClaim, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-final-attempt",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)
	require.Equal(t, doc.SearchDocumentID, firstClaim[0].SearchDocumentID)

	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET max_attempts = attempts,
			    lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, firstClaim[0].EmbeddingJobID).Error
	})
	require.NoError(t, err)

	nextClaim, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-next",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, nextClaim)

	var jobStatus string
	var jobCompleted bool
	var jobError string
	var documentState string
	var documentError string
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT job.status, job.completed_at IS NOT NULL, job.error,
			       document.search_state, document.embedding_error
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id
			 AND document.search_document_id = job.search_document_id
			WHERE job.team_id = ?::uuid
			  AND job.embedding_job_id = ?::uuid
		`, teamID, firstClaim[0].EmbeddingJobID).Row().Scan(
			&jobStatus,
			&jobCompleted,
			&jobError,
			&documentState,
			&documentError,
		)
	})
	require.NoError(t, err)
	assert.Equal(t, "failed", jobStatus)
	assert.True(t, jobCompleted)
	assert.Equal(t, v2EmbeddingJobAttemptsExhaustedMessage, jobError)
	assert.Equal(t, "failed", documentState)
	assert.Equal(t, v2EmbeddingJobAttemptsExhaustedMessage, documentError)
}

func TestV2SearchExactVectorRequiresBoundedContract(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-exact-policy-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-exact-policy-owner")
	insertV2SearchTestContractWithOptions(t, adminDB, rls, "search-hnsw-blocked", 3, "halfvec_hnsw", "missing_exact_blocked_idx", 10000, false)
	repo := NewV2SearchRepository(appDB, rls)

	_, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchContractMismatch), "err=%v", err)
	assert.Contains(t, err.Error(), "does not allow exact vector search")

	insertV2SearchTestContractWithOptions(t, adminDB, rls, "search-bounded", 3, "exact", "", 1, false)
	docA := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "bounded vector one", 1)
	docB := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "bounded vector two", 1)
	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		docA.SearchDocumentID: {1, 0, 0},
		docB.SearchDocumentID: {0, 1, 0},
	})

	_, err = repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchContractMismatch), "err=%v", err)
	assert.Contains(t, err.Error(), "exceed contract max")
}

func TestV2SearchDocumentStateRejectsStaleProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-state-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-state-owner")
	contractID := insertV2SearchTestContract(t, adminDB, rls, "search-state", 3, "exact", "")

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, teamID, ownerID); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_documents (
			    team_id, owner_profile_id, source_kind, source_id, source_version,
			    document_version, embedding_contract_id, embedding_dimensions,
			    search_state, document_text, document_hash
			) VALUES (
			    ?::uuid, ?::uuid, 'evidence', ?::uuid, 1, 1,
			    ?::uuid, 3, 'stale', 'stale projection text', 'sha256:stale'
			)
		`, teamID, ownerID, uuid.NewString(), contractID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search_documents_state_check")
}

func TestV2SearchReadinessAndHNSWPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-hnsw-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-hnsw-owner")
	repo := NewV2SearchRepository(appDB, rls)
	readyContractID := insertV2SearchTestContract(t, adminDB, rls, "search-ready", 3, "halfvec_hnsw", v2SearchTestHNSWIndexName)
	createV2SearchTestHNSWIndex(t, adminDB, rls, readyContractID)

	ready, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.True(t, ready.Ready, "readiness reasons: %+v", ready.Reasons)
	require.NotNil(t, ready.Contract)
	assert.Equal(t, "halfvec_hnsw", ready.Contract.IndexStrategy)
	assert.Equal(t, 3, ready.Contract.EmbeddingDimensions)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "test generation hnsw text", 1)
	otherDoc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "test generation farther hnsw text", 1)
	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		doc.SearchDocumentID:      unitV2SearchVector(3, 0),
		otherDoc.SearchDocumentID: unitV2SearchVector(3, 1),
	})
	query, err := v2VectorLiteral(unitV2SearchVector(3, 0))
	require.NoError(t, err)
	var recallHits []V2SearchHit
	err = repo.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		var err error
		recallHits, err = searchV2RecallVector(ctx, tx, V2RecallEvidenceInput{
			TeamID:         teamID,
			QueryEmbedding: unitV2SearchVector(3, 0),
			Limit:          2,
		}, ready.Contract, 2)
		return err
	})
	require.NoError(t, err)
	require.Len(t, recallHits, 2)
	assert.Equal(t, doc.SearchDocumentID, recallHits[0].SearchDocumentID)
	assert.Less(t, recallHits[0].Distance, recallHits[1].Distance)

	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`SET LOCAL enable_seqscan = off`).Error)
		rows, err := tx.Raw(`
			EXPLAIN (COSTS OFF)
			SELECT search_document_id
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = 3
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			ORDER BY embedding::halfvec(3) <=> ?::halfvec(3)
			LIMIT 1
		`, teamID, readyContractID, query).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		require.NoError(t, rows.Err())
		assert.Contains(t, strings.Join(plan, "\n"), v2SearchTestHNSWIndexName)
		return nil
	})
	require.NoError(t, err)

	insertV2SearchTestContract(t, adminDB, rls, "search-missing", 3, "halfvec_hnsw", "missing_v2_search_hnsw_idx")
	missing, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.False(t, missing.Ready)
	require.NotEmpty(t, missing.Reasons)
	assert.Equal(t, "missing_physical_index", missing.Reasons[0].Code)

	insertV2SearchTestContract(
		t,
		adminDB,
		rls,
		"search-incompatible",
		4,
		"halfvec_hnsw",
		v2SearchTestHNSWIndexName,
	)
	incompatible, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.False(t, incompatible.Ready)
	require.NotEmpty(t, incompatible.Reasons)
	assert.Equal(t, "incompatible_physical_index", incompatible.Reasons[0].Code)
	assert.Contains(t, incompatible.Reasons[0].Message, "indexed expression")
}

func createV2SearchTestHNSWIndex(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	contractID string,
) {
	t.Helper()
	parsedContractID, err := uuid.Parse(contractID)
	require.NoError(t, err)
	err = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS v2_search_documents_test_3_halfvec_hnsw_idx
			    ON search_documents
			    USING hnsw ((embedding::halfvec(3)) halfvec_cosine_ops)
			    WITH (m = 16, ef_construction = 64)
			    WHERE embedding_contract_id = '%s'::uuid
			      AND embedding_dimensions = 3
			      AND search_state = 'current'
			      AND embedding IS NOT NULL
		`, parsedContractID.String())).Error
	})
	require.NoError(t, err)
}

func insertV2SearchTestContract(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
) string {
	return insertV2SearchTestContractWithOptions(t, db, rls, prefix, dimensions, strategy, indexName, 10000, false)
}

func insertV2SearchTestContractWithOptions(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
	exactMaxRows int,
	allowExactFallback bool,
) string {
	t.Helper()
	sequence := int(v2SearchTestContractSequence.Add(1))
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID := uuid.NewString()
	searchGenerationID := uuid.NewString()
	operatorClass := ""
	indexedExpression := ""
	switch strategy {
	case "vector_hnsw":
		operatorClass = "vector_cosine_ops"
		indexedExpression = fmt.Sprintf("embedding::vector(%d)", dimensions)
	case "halfvec_hnsw":
		operatorClass = "halfvec_cosine_ops"
		indexedExpression = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	}
	err := rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO embedding_contracts (
			    embedding_contract_id, contract_key, version, provider, model,
				    dimensions, distance_metric, vector_normalization,
				    document_format_version, query_format_version, lifecycle_state
				) VALUES (
				    ?::uuid, ?, ?, 'test', ?, ?, 'cosine', 'provider', 1, 1, 'active'
				)
			`, contractID, contractKey, sequence, "test-model", dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (
			    search_index_generation_id, generation, embedding_contract_id,
			    embedding_dimensions, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, exact_max_rows,
			    allow_exact_fallback, activation_state, activated_at
			) VALUES (
			    ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, 'active', now()
			)
		`, searchGenerationID, sequence, contractID, dimensions, strategy,
			operatorClass, indexedExpression, indexName, exactMaxRows, allowExactFallback).Error
	})
	require.NoError(t, err)
	return contractID
}

func upsertV2SearchDocumentForTest(
	t *testing.T,
	repo *V2SearchRepositoryImpl,
	teamID string,
	ownerID string,
	text string,
	sourceVersion int64,
) *V2SearchDocumentResult {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(context.Background(), V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       uuid.NewString(),
		SourceVersion:  sourceVersion,
		DocumentText:   text,
	})
	require.NoError(t, err)
	require.NotEmpty(t, doc.SearchDocumentID)
	require.Equal(t, "pending", doc.SearchState)
	require.NotEmpty(t, doc.QueuedJobID)
	return doc
}

func completeV2SearchJobsForTest(
	t *testing.T,
	repo *V2SearchRepositoryImpl,
	teamID string,
	vectorsByDocumentID map[string][]float32,
) {
	t.Helper()
	workerID := "worker-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	jobs, err := repo.ClaimEmbeddingJobs(context.Background(), V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: workerID,
		Limit:    len(vectorsByDocumentID) + 5,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	completed := 0
	for _, job := range jobs {
		vector, ok := vectorsByDocumentID[job.SearchDocumentID]
		if !ok {
			continue
		}
		err := repo.CompleteEmbeddingJob(context.Background(), V2CompleteEmbeddingJobInput{
			TeamID:           teamID,
			EmbeddingJobID:   job.EmbeddingJobID,
			WorkerID:         workerID,
			ExpectedAttempts: job.Attempts,
			Embedding:        vector,
		})
		require.NoError(t, err)
		completed++
	}
	require.Equal(t, len(vectorsByDocumentID), completed)
}

func unitV2SearchVector(dimensions int, oneAt int) []float32 {
	vector := make([]float32, dimensions)
	vector[oneAt] = 1
	return vector
}
