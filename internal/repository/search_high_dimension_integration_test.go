package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const searchTestBinaryHNSWIndexName = "search_documents_test_4096_binary_hnsw_idx"

func TestEnsureActiveSearchContractPromotesHighDimensionExactGeneration(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	contractID := insertSearchTestContract(t, adminDB, rls, "search-binary-transition", 4096, "exact", "")
	repo := NewSearchRepository(adminDB, rls)
	input := normalizeEnsureActiveSearchContractInput(EnsureActiveSearchContractInput{
		Provider:   "test",
		Model:      "test-model",
		Dimensions: 4096,
	})

	result, err := repo.EnsureActiveSearchContract(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, result.Contract)
	assert.Equal(t, contractID, result.Contract.EmbeddingContractID)
	assert.Equal(t, string(domain.VectorIndexBinaryHNSW), result.Contract.IndexStrategy)
	assert.True(t, result.CreatedGeneration)
	assert.True(t, result.CreatedPhysicalIndex)
	defer func() {
		err := adminDB.Exec("DROP INDEX IF EXISTS " + pq.QuoteIdentifier(result.Contract.PhysicalIndexName)).Error
		assert.NoError(t, err)
	}()

	var generations []struct {
		AnnStrategy     string
		ActivationState string
	}
	require.NoError(t, adminDB.Raw(`
		SELECT ann_strategy, activation_state
		FROM search_index_generations
		WHERE embedding_contract_id = ?::uuid
		ORDER BY generation ASC, created_at ASC, search_index_generation_id ASC
	`, contractID).Scan(&generations).Error)
	require.Len(t, generations, 2)
	assert.Equal(t, "exact", generations[0].AnnStrategy)
	assert.Equal(t, "deprecated", generations[0].ActivationState)
	assert.Equal(t, "binary_hnsw", generations[1].AnnStrategy)
	assert.Equal(t, "active", generations[1].ActivationState)

	var valid bool
	require.NoError(t, adminDB.Raw(`
		SELECT index_meta.indisvalid
		FROM pg_index AS index_meta
		JOIN pg_class AS index_class ON index_class.oid = index_meta.indexrelid
		WHERE index_class.relname = ?
	`, result.Contract.PhysicalIndexName).Scan(&valid).Error)
	assert.True(t, valid)
}

func TestEnsureActiveSearchContractSupports16000BinaryHNSW(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSearchRepository(adminDB, rls)

	result, err := repo.EnsureActiveSearchContract(ctx, EnsureActiveSearchContractInput{
		Provider:   "test",
		Model:      "test-model-16000",
		Dimensions: 16000,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Contract)
	assert.Equal(t, string(domain.VectorIndexBinaryHNSW), result.Contract.IndexStrategy)
	assert.Equal(t, 16000, result.Contract.EmbeddingDimensions)

	var indexValid bool
	require.NoError(t, adminDB.Raw(`
		SELECT index.indisvalid
		FROM pg_class AS relation
		JOIN pg_index AS index ON index.indexrelid = relation.oid
		WHERE relation.relname = ?
	`, result.Contract.PhysicalIndexName).Scan(&indexValid).Error)
	assert.True(t, indexValid)
}

func TestEnsureActiveSearchContractRejectsMultipleActiveGenerations(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSearchRepository(adminDB, rls)
	input := EnsureActiveSearchContractInput{
		Provider:   "test",
		Model:      "test-model-duplicate-active",
		Dimensions: 3,
	}

	active, err := repo.EnsureActiveSearchContract(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, active.Contract)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO search_index_generations (
			    search_index_generation_id, generation, embedding_contract_id,
			    embedding_dimensions, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, hnsw_m,
			    hnsw_ef_construction, query_ef_search, exact_max_rows,
			    candidate_limit, allow_exact_fallback, activation_state,
			    activated_at, metadata
			)
			SELECT ?::uuid, generation + 1, embedding_contract_id,
			       embedding_dimensions, ann_strategy, operator_class,
			       indexed_expression, physical_index_name, hnsw_m,
			       hnsw_ef_construction, query_ef_search, exact_max_rows,
			       candidate_limit, allow_exact_fallback, 'active', now(), metadata
			FROM search_index_generations
			WHERE search_index_generation_id = ?::uuid
		`, uuid.NewString(), active.Contract.SearchIndexGenerationID).Error
	}))

	_, err = repo.EnsureActiveSearchContract(ctx, input)
	require.ErrorIs(t, err, ErrSearchContractMismatch)
	assert.Contains(t, err.Error(), "expected one active search generation, found 2")
}

func TestSearchReadinessRejectsInvalidPhysicalIndex(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	const (
		tableName    = "search_invalid_index_target"
		functionName = "search_invalid_index_failure"
		indexName    = "search_invalid_physical_index"
	)
	require.NoError(t, adminDB.Exec(`CREATE TABLE `+tableName+` (id integer NOT NULL)`).Error)
	require.NoError(t, adminDB.Exec(`INSERT INTO `+tableName+` (id) VALUES (1)`).Error)
	require.NoError(t, adminDB.Exec(`
		CREATE FUNCTION `+functionName+`(value integer) RETURNS integer
		LANGUAGE plpgsql IMMUTABLE AS $$
		BEGIN
			RAISE EXCEPTION 'intentional index build failure';
		END;
		$$
	`).Error)
	sqlDB, err := adminDB.DB()
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, `
		CREATE INDEX CONCURRENTLY `+indexName+`
		ON `+tableName+` (`+functionName+`(id))
	`)
	require.Error(t, err)

	contractID := insertSearchTestContract(t, adminDB, rls, "search-invalid-index", 3, "vector_hnsw", indexName)
	repo := NewSearchRepository(adminDB, rls)
	readiness, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Len(t, readiness.Reasons, 1)
	assert.Equal(t, "invalid_physical_index", readiness.Reasons[0].Code)

	_, err = repo.ensureSearchPhysicalIndex(ctx, readiness.Contract)
	require.ErrorIs(t, err, ErrSearchContractMismatch)
	assert.Contains(t, err.Error(), indexName)
	assert.NotEmpty(t, contractID)
}

func TestEnsureActiveSearchContractSerializesHighDimensionPromotion(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	contractID := insertSearchTestContract(t, adminDB, rls, "search-binary-concurrent", 4096, "exact", "")
	input := normalizeEnsureActiveSearchContractInput(EnsureActiveSearchContractInput{
		Provider:   "test",
		Model:      "test-model",
		Dimensions: 4096,
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := NewSearchRepository(adminDB, rls).EnsureActiveSearchContract(ctx, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var generations []struct {
		AnnStrategy     string
		ActivationState string
		PhysicalName    string `gorm:"column:physical_index_name"`
	}
	require.NoError(t, adminDB.Raw(`
		SELECT ann_strategy, activation_state, physical_index_name
		FROM search_index_generations
		WHERE embedding_contract_id = ?::uuid
		ORDER BY generation ASC, created_at ASC, search_index_generation_id ASC
	`, contractID).Scan(&generations).Error)
	require.Len(t, generations, 2)
	assert.Equal(t, "exact", generations[0].AnnStrategy)
	assert.Equal(t, "deprecated", generations[0].ActivationState)
	assert.Equal(t, "binary_hnsw", generations[1].AnnStrategy)
	assert.Equal(t, "active", generations[1].ActivationState)
	require.NotEmpty(t, generations[1].PhysicalName)
	defer func() {
		err := adminDB.Exec("DROP INDEX IF EXISTS " + pq.QuoteIdentifier(generations[1].PhysicalName)).Error
		assert.NoError(t, err)
	}()
}

func TestSearchReadinessAndBinaryHNSWPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-binary-hnsw-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-binary-hnsw-owner")
	repo := NewSearchRepository(appDB, rls)
	contractID := insertSearchTestContract(t, adminDB, rls, "search-binary-ready", 4096, "binary_hnsw", searchTestBinaryHNSWIndexName)
	createSearchTestBinaryHNSWIndex(t, adminDB, rls, contractID, 4096)

	ready, err := repo.CheckSearchReadiness(ctx)
	require.NoError(t, err)
	require.True(t, ready.Ready, "readiness reasons: %+v", ready.Reasons)
	require.NotNil(t, ready.Contract)
	assert.Equal(t, "binary_hnsw", ready.Contract.IndexStrategy)
	assert.Equal(t, 4096, ready.Contract.EmbeddingDimensions)

	doc := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "binary hnsw nearest vector", 1)
	otherDoc := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "binary hnsw farther vector", 1)
	queryVector := unitSearchVector(4096, 0)
	completeSearchJobsForTest(t, repo, teamID, map[string][]float32{
		doc.SearchDocumentID:      queryVector,
		otherDoc.SearchDocumentID: unitSearchVector(4096, 1),
	})

	var recallHits []SearchHit
	err = repo.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		var err error
		recallHits, err = searchRecallVector(ctx, tx, RecallEvidenceInput{
			TeamID:         teamID,
			QueryEmbedding: queryVector,
			Limit:          2,
		}, ready.Contract, 2)
		return err
	})
	require.NoError(t, err)
	require.Len(t, recallHits, 2)
	assert.Equal(t, doc.SearchDocumentID, recallHits[0].SearchDocumentID)
	assert.Less(t, recallHits[0].Distance, recallHits[1].Distance)

	query, err := vectorLiteral(queryVector)
	require.NoError(t, err)
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`SET LOCAL enable_seqscan = off`).Error)
		rows, err := tx.Raw(`
			EXPLAIN (COSTS OFF)
			SELECT search_document_id
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = 4096
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			ORDER BY binary_quantize(embedding)::bit(4096) <~> binary_quantize(?::vector)::bit(4096)
			LIMIT 1
		`, teamID, contractID, query).Rows()
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
		assert.Contains(t, strings.Join(plan, "\n"), searchTestBinaryHNSWIndexName)
		return nil
	})
	require.NoError(t, err)
}

func createSearchTestBinaryHNSWIndex(
	t *testing.T,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	contractID string,
	dimensions int,
) {
	t.Helper()
	parsedContractID, err := uuid.Parse(contractID)
	require.NoError(t, err)
	err = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS %s
			    ON search_documents
			    USING hnsw ((binary_quantize(embedding)::bit(%d)) bit_hamming_ops)
			    WITH (m = 16, ef_construction = 64)
			    WHERE embedding_contract_id = '%s'::uuid
			      AND embedding_dimensions = %d
			      AND search_state = 'current'
			      AND embedding IS NOT NULL
		`, searchTestBinaryHNSWIndexName, dimensions, parsedContractID.String(), dimensions)).Error
	})
	require.NoError(t, err)
}
