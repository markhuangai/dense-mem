package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const defaultV2SearchContractID = "00000000-0000-0000-0000-000000020001"

func TestV2SearchDocumentsFTSAndExactVectorAreTeamScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "search-team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "search-owner-a")
	teamC := createV2LedgerTeam(t, adminDB, rls, "search-team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "search-owner-c")
	profileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-exact", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	docA := upsertV2SearchDocumentForTest(t, repo, teamA, ownerA, profileKey, "postgres pgvector durable memory", 1)
	docB := upsertV2SearchDocumentForTest(t, repo, teamA, ownerA, profileKey, "pgvector ranking contract", 1)
	docC := upsertV2SearchDocumentForTest(t, repo, teamC, ownerC, profileKey, "postgres pgvector other team", 1)
	completeV2SearchJobsForTest(t, repo, teamA, map[string][]float32{
		docA.SearchDocumentID: {1, 0, 0},
		docB.SearchDocumentID: {0, 1, 0},
	})
	completeV2SearchJobsForTest(t, repo, teamC, map[string][]float32{
		docC.SearchDocumentID: {1, 0, 0},
	})

	vectorHits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamA,
		ProfileKey:     profileKey,
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

func TestV2SearchEmbeddingCompletionRejectsStaleJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-stale-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-stale-owner")
	profileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-stale", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)
	sourceID := uuid.NewString()

	first, err := repo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ProfileKey:     profileKey,
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
		ProfileKey:     profileKey,
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

	hits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		ProfileKey:     profileKey,
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
		ProfileKey:     profileKey,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, int64(2), hits[0].SourceVersion)
	assert.Equal(t, int64(2), hits[0].DocumentVersion)
}

func TestV2EmbeddingJobsRecoverExpiredLeasesAndBoundRetries(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-lease-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-lease-owner")
	profileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-lease", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, profileKey, "lease recovery document", 1)
	claimedByA, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-a",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedByA, 1)
	require.Equal(t, doc.SearchDocumentID, claimedByA[0].SearchDocumentID)
	require.Equal(t, 1, claimedByA[0].Attempts)

	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET lease_until = now() - interval '1 second'
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, claimedByA[0].EmbeddingJobID).Error
	}))

	claimedByB, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-b",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedByB, 1)
	require.Equal(t, claimedByA[0].EmbeddingJobID, claimedByB[0].EmbeddingJobID)
	require.Equal(t, 2, claimedByB[0].Attempts)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   claimedByA[0].EmbeddingJobID,
		WorkerID:         "worker-a",
		ExpectedAttempts: claimedByA[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2EmbeddingLeaseLost), "err=%v", err)

	failed, err := repo.FailEmbeddingJob(ctx, V2FailEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   claimedByB[0].EmbeddingJobID,
		WorkerID:         "worker-b",
		ExpectedAttempts: claimedByB[0].Attempts,
		Error:            strings.Repeat("provider timeout ", 80),
		RetryAfter:       2 * time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, failed)
	assert.Equal(t, "queued", failed.Status)
	assert.False(t, failed.Terminal)
	assert.Equal(t, 2*time.Minute, failed.RetryAfter)

	state := loadV2EmbeddingJobState(t, adminDB, rls, teamID, claimedByB[0].EmbeddingJobID)
	assert.Equal(t, "queued", state.JobStatus)
	assert.Equal(t, "pending", state.SearchState)
	assert.LessOrEqual(t, len(state.Error), 512)

	claimedTooEarly, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-c",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, claimedTooEarly)

	stats, err := repo.GetEmbeddingQueueStats(ctx, V2EmbeddingQueueStatsInput{
		TeamID:              teamID,
		EmbeddingContractID: doc.EmbeddingContractID,
		EmbeddingDimensions: doc.EmbeddingDimensions,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.Queued)
	assert.True(t, stats.CutoverBlocking)

	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE embedding_jobs
			SET available_at = now()
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
		`, teamID, claimedByB[0].EmbeddingJobID).Error
	}))
	claimedByC, err := repo.ClaimEmbeddingJobs(ctx, V2ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "worker-c",
		Limit:    1,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedByC, 1)
	require.Equal(t, 3, claimedByC[0].Attempts)

	terminal, err := repo.FailEmbeddingJob(ctx, V2FailEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   claimedByC[0].EmbeddingJobID,
		WorkerID:         "worker-c",
		ExpectedAttempts: claimedByC[0].Attempts,
		Error:            "provider permanent failure",
		Terminal:         true,
	})
	require.NoError(t, err)
	require.NotNil(t, terminal)
	assert.Equal(t, "failed", terminal.Status)
	assert.True(t, terminal.Terminal)

	state = loadV2EmbeddingJobState(t, adminDB, rls, teamID, claimedByC[0].EmbeddingJobID)
	assert.Equal(t, "failed", state.JobStatus)
	assert.Equal(t, "failed", state.SearchState)
	assert.True(t, state.CompletedAt.Valid)

	stats, err = repo.GetEmbeddingQueueStats(ctx, V2EmbeddingQueueStatsInput{
		TeamID:              teamID,
		EmbeddingContractID: doc.EmbeddingContractID,
		EmbeddingDimensions: doc.EmbeddingDimensions,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.TerminalFailures)
	assert.True(t, stats.CutoverBlocking)
}

func TestV2SearchReadinessAndHNSWPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-hnsw-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-hnsw-owner")
	repo := NewV2SearchRepository(appDB, rls)

	ready, err := repo.CheckSearchReadiness(ctx, "default")
	require.NoError(t, err)
	require.True(t, ready.Ready, "readiness reasons: %+v", ready.Reasons)
	require.NotNil(t, ready.Profile)
	assert.Equal(t, "halfvec_hnsw", ready.Profile.IndexStrategy)

	missingProfileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-missing", 3, "halfvec_hnsw", "missing_v2_search_hnsw_idx")
	missing, err := repo.CheckSearchReadiness(ctx, missingProfileKey)
	require.NoError(t, err)
	require.False(t, missing.Ready)
	require.NotEmpty(t, missing.Reasons)
	assert.Equal(t, "missing_physical_index", missing.Reasons[0].Code)

	incompatibleProfileKey, _ := insertV2SearchTestProfile(
		t,
		adminDB,
		rls,
		"search-incompatible",
		3,
		"halfvec_hnsw",
		"v2_search_documents_default_1536_halfvec_hnsw_idx",
	)
	incompatible, err := repo.CheckSearchReadiness(ctx, incompatibleProfileKey)
	require.NoError(t, err)
	require.False(t, incompatible.Ready)
	require.NotEmpty(t, incompatible.Reasons)
	assert.Equal(t, "incompatible_physical_index", incompatible.Reasons[0].Code)
	assert.Contains(t, incompatible.Reasons[0].Message, "indexed expression")

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, "default", "default profile hnsw text", 1)
	blocked, err := repo.CheckSearchReadiness(ctx, "default")
	require.NoError(t, err)
	require.False(t, blocked.Ready)
	require.NotEmpty(t, blocked.Reasons)
	assert.Equal(t, "embedding_backlog_pending", blocked.Reasons[len(blocked.Reasons)-1].Code)

	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		doc.SearchDocumentID: unitV2SearchVector(1536, 0),
	})
	readyAfterDrain, err := repo.CheckSearchReadiness(ctx, "default")
	require.NoError(t, err)
	require.True(t, readyAfterDrain.Ready, "readiness reasons: %+v", readyAfterDrain.Reasons)

	query, err := v2VectorLiteral(unitV2SearchVector(1536, 0))
	require.NoError(t, err)
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`SET LOCAL enable_seqscan = off`).Error)
		rows, err := tx.Raw(`
			EXPLAIN (COSTS OFF)
			SELECT search_document_id
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND embedding_contract_id = '00000000-0000-0000-0000-000000020001'::uuid
			  AND embedding_dimensions = 1536
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			ORDER BY embedding::halfvec(1536) <=> ?::halfvec(1536)
			LIMIT 1
		`, teamID, query).Rows()
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
		assert.Contains(t, strings.Join(plan, "\n"), "v2_search_documents_default_1536_halfvec_hnsw_idx")
		return nil
	})
	require.NoError(t, err)
}

func insertV2SearchTestProfile(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
) (string, string) {
	t.Helper()
	profileKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID := uuid.NewString()
	searchProfileID := uuid.NewString()
	rankingProfileID := uuid.NewString()
	operatorClass := ""
	indexedExpression := ""
	if strategy != "exact" {
		operatorClass = "halfvec_cosine_ops"
		indexedExpression = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	}
	err := rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO embedding_contracts (
			    embedding_contract_id, contract_key, version, provider, model,
			    dimensions, distance_metric, vector_normalization,
			    document_format_version, query_format_version, lifecycle_state
			) VALUES (
			    ?::uuid, ?, 1, 'test', ?, ?, 'cosine', 'provider', 1, 1, 'active'
			)
		`, contractID, profileKey, "test-model", dimensions).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO search_index_profiles (
			    search_index_profile_id, profile_key, version, embedding_contract_id,
			    embedding_dimensions, distance_metric, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, activation_state, activated_at
			) VALUES (
			    ?::uuid, ?, 1, ?::uuid, ?, 'cosine', ?, ?, ?, ?, 'active', now()
			)
		`, searchProfileID, profileKey, contractID, dimensions, strategy,
			operatorClass, indexedExpression, indexName).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO ranking_profiles (
			    ranking_profile_id, profile_key, version, fusion_mode, rrf_k,
			    branch_weights, branch_order, activation_state, activated_at
			) VALUES (
			    ?::uuid, ?, 1, 'rrf', 60, '{}'::jsonb,
			    ARRAY['full_text','vector_exact']::text[], 'active', now()
			)
		`, rankingProfileID, profileKey).Error
	})
	require.NoError(t, err)
	return profileKey, contractID
}

func upsertV2SearchDocumentForTest(
	t *testing.T,
	repo *V2SearchRepositoryImpl,
	teamID string,
	ownerID string,
	profileKey string,
	text string,
	sourceVersion int64,
) *V2SearchDocumentResult {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(context.Background(), V2UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ProfileKey:     profileKey,
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

type v2EmbeddingJobState struct {
	JobStatus   string
	SearchState string
	Error       string
	CompletedAt sql.NullTime
}

func loadV2EmbeddingJobState(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	teamID string,
	embeddingJobID string,
) v2EmbeddingJobState {
	t.Helper()
	var state v2EmbeddingJobState
	err := rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT job.status, document.search_state, job.error, job.completed_at
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id
			 AND document.search_document_id = job.search_document_id
			WHERE job.team_id = ?::uuid
			  AND job.embedding_job_id = ?::uuid
		`, teamID, embeddingJobID).Row().Scan(
			&state.JobStatus,
			&state.SearchState,
			&state.Error,
			&state.CompletedAt,
		)
	})
	require.NoError(t, err)
	return state
}

func unitV2SearchVector(dimensions int, oneAt int) []float32 {
	vector := make([]float32, dimensions)
	vector[oneAt] = 1
	return vector
}
