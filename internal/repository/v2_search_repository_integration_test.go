package repository

import (
	"context"
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

func TestV2SearchExactVectorUsesProfileDistanceMetric(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-metric-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-metric-owner")
	repo := NewV2SearchRepository(appDB, rls)

	tests := []struct {
		name           string
		metric         string
		query          []float32
		expectedVector []float32
		otherVector    []float32
	}{
		{
			name:           "l2",
			metric:         "l2",
			query:          []float32{1, 0, 0},
			expectedVector: []float32{1, 1, 0},
			otherVector:    []float32{10, 0, 0},
		},
		{
			name:           "inner_product",
			metric:         "inner_product",
			query:          []float32{1, 0, 0},
			expectedVector: []float32{2, 2, 0},
			otherVector:    []float32{1, 0, 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profileKey, _ := insertV2SearchTestProfileWithMetric(t, adminDB, rls, "search-"+tt.name, 3, "exact", "", tt.metric)
			expected := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, profileKey, tt.name+" expected metric result", 1)
			other := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, profileKey, tt.name+" cosine-biased result", 1)
			completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
				expected.SearchDocumentID: tt.expectedVector,
				other.SearchDocumentID:    tt.otherVector,
			})

			hits, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
				TeamID:         teamID,
				ProfileKey:     profileKey,
				QueryEmbedding: tt.query,
				Limit:          2,
			})
			require.NoError(t, err)
			require.Len(t, hits, 2)
			assert.Equal(t, expected.SearchDocumentID, hits[0].SearchDocumentID)
			assert.Less(t, hits[0].Distance, hits[1].Distance)
		})
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
		TeamID:         teamID,
		EmbeddingJobID: claimed[0].EmbeddingJobID,
		WorkerID:       "worker-stale",
		Embedding:      []float32{1, 0, 0},
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

func TestV2SearchClaimEmbeddingJobsReclaimsExpiredLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-reclaim-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-reclaim-owner")
	profileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-reclaim", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, profileKey, "lease reclaim text", 1)
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
		TeamID:         teamID,
		EmbeddingJobID: firstClaim[0].EmbeddingJobID,
		WorkerID:       "worker-one",
		Embedding:      []float32{1, 0, 0},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchStaleVersion), "err=%v", err)

	err = repo.CompleteEmbeddingJob(ctx, V2CompleteEmbeddingJobInput{
		TeamID:         teamID,
		EmbeddingJobID: secondClaim[0].EmbeddingJobID,
		WorkerID:       "worker-two",
		Embedding:      []float32{1, 0, 0},
	})
	require.NoError(t, err)
}

func TestV2SearchClaimEmbeddingJobsFailsExpiredExhaustedJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-exhausted-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-exhausted-owner")
	profileKey, _ := insertV2SearchTestProfile(t, adminDB, rls, "search-exhausted", 3, "exact", "")
	repo := NewV2SearchRepository(appDB, rls)

	doc := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, profileKey, "exhausted embedding job text", 1)
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

func TestV2SearchExactVectorRequiresBoundedProfile(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-exact-policy-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-exact-policy-owner")
	boundedProfileKey, _ := insertV2SearchTestProfileWithOptions(t, adminDB, rls, "search-bounded", 3, "exact", "", 1, false)
	repo := NewV2SearchRepository(appDB, rls)

	_, err := repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		ProfileKey:     "default",
		QueryEmbedding: unitV2SearchVector(1536, 0),
		Limit:          10,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchProfileMismatch), "err=%v", err)
	assert.Contains(t, err.Error(), "does not allow exact vector search")

	docA := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, boundedProfileKey, "bounded vector one", 1)
	docB := upsertV2SearchDocumentForTest(t, repo, teamID, ownerID, boundedProfileKey, "bounded vector two", 1)
	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		docA.SearchDocumentID: {1, 0, 0},
		docB.SearchDocumentID: {0, 1, 0},
	})

	_, err = repo.SearchExactVector(ctx, V2ExactVectorSearchInput{
		TeamID:         teamID,
		ProfileKey:     boundedProfileKey,
		QueryEmbedding: []float32{1, 0, 0},
		Limit:          10,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchProfileMismatch), "err=%v", err)
	assert.Contains(t, err.Error(), "exceed profile max")
}

func TestV2SearchDocumentStateRejectsStaleProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "search-state-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "search-state-owner")

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
			    ?::uuid, 1536, 'stale', 'stale projection text', 'sha256:stale'
			)
		`, teamID, ownerID, uuid.NewString(), defaultV2SearchContractID).Error
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
	completeV2SearchJobsForTest(t, repo, teamID, map[string][]float32{
		doc.SearchDocumentID: unitV2SearchVector(1536, 0),
	})
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
	return insertV2SearchTestProfileWithOptions(t, db, rls, prefix, dimensions, strategy, indexName, 10000, false)
}

func insertV2SearchTestProfileWithMetric(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
	metric string,
) (string, string) {
	return insertV2SearchTestProfileWithOptionsAndMetric(t, db, rls, prefix, dimensions, strategy, indexName, 10000, false, metric)
}

func insertV2SearchTestProfileWithOptions(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
	exactMaxRows int,
	allowExactFallback bool,
) (string, string) {
	return insertV2SearchTestProfileWithOptionsAndMetric(t, db, rls, prefix, dimensions, strategy, indexName, exactMaxRows, allowExactFallback, "cosine")
}

func insertV2SearchTestProfileWithOptionsAndMetric(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	prefix string,
	dimensions int,
	strategy string,
	indexName string,
	exactMaxRows int,
	allowExactFallback bool,
	metric string,
) (string, string) {
	t.Helper()
	profileKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID := uuid.NewString()
	searchProfileID := uuid.NewString()
	rankingProfileID := uuid.NewString()
	operatorClass := ""
	indexedExpression := ""
	if strategy != "exact" {
		operatorClass = v2SearchTestHalfvecOperatorClass(metric)
		indexedExpression = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	}
	err := rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO embedding_contracts (
			    embedding_contract_id, contract_key, version, provider, model,
				    dimensions, distance_metric, vector_normalization,
				    document_format_version, query_format_version, lifecycle_state
				) VALUES (
				    ?::uuid, ?, 1, 'test', ?, ?, ?, 'provider', 1, 1, 'active'
				)
			`, contractID, profileKey, "test-model", dimensions, metric).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO search_index_profiles (
			    search_index_profile_id, profile_key, version, embedding_contract_id,
			    embedding_dimensions, distance_metric, ann_strategy, operator_class,
				    indexed_expression, physical_index_name, exact_max_rows,
				    allow_exact_fallback, activation_state, activated_at
				) VALUES (
				    ?::uuid, ?, 1, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, 'active', now()
				)
			`, searchProfileID, profileKey, contractID, dimensions, metric, strategy,
			operatorClass, indexedExpression, indexName, exactMaxRows, allowExactFallback).Error; err != nil {
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

func v2SearchTestHalfvecOperatorClass(metric string) string {
	switch metric {
	case "l2":
		return "halfvec_l2_ops"
	case "inner_product":
		return "halfvec_ip_ops"
	default:
		return "halfvec_cosine_ops"
	}
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
			TeamID:         teamID,
			EmbeddingJobID: job.EmbeddingJobID,
			WorkerID:       workerID,
			Embedding:      vector,
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
