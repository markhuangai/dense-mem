package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/markhuangai/dense-mem/internal/domain"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchAdapterEnsureActiveContractExistingGeneration(t *testing.T) {
	input := bootstrapInput(2)
	contractID := "11111111-1111-1111-1111-111111111111"
	spec := deriveSearchGenerationSpec(contractID, input)
	generationID := "77777777-7777-7777-7777-777777777777"
	contract := &searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, SearchIndexGenerationID: generationID,
		EmbeddingDimensions: input.Dimensions, EmbeddingProvider: input.Provider, EmbeddingModel: input.Model,
		DistanceMetric: string(domain.VectorDistanceCosine), VectorNormalization: input.VectorNormalization,
		DocumentFormatVersion: 1, QueryFormatVersion: 1, IndexGeneration: 1,
		IndexStrategy: spec.AnnStrategy, OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, QueryEFSearch: spec.QueryEFSearch,
		ExactMaxRows: input.ExactMaxRows, CandidateLimit: input.CandidateLimit, AllowExactFallback: spec.AllowExactFallback,
	}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: generationID, Generation: 1, EmbeddingContractID: contractID,
		EmbeddingDimensions: 2, AnnStrategy: spec.AnnStrategy, OperatorClass: spec.OperatorClass,
		IndexedExpression: spec.IndexedExpression, PhysicalIndexName: spec.PhysicalIndexName,
		HNSWM: 16, HNSWEFConstruction: 64, QueryEFSearch: spec.QueryEFSearch,
		ExactMaxRows: input.ExactMaxRows, CandidateLimit: input.CandidateLimit,
		AllowExactFallback: spec.AllowExactFallback, ActivationState: "active",
	}, 1))
	expectActiveContract(mock, contract)
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}).AddRow(true,
		"using hnsw (embedding::vector(2) vector_cosine_ops) embedding_contract_id = '"+contractID+"' embedding_dimensions = 2 search_state = 'current' embedding is not null"))
	result, err := store.EnsureActiveSearchContract(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.CreatedContract)
	assert.False(t, result.CreatedGeneration)
	assert.False(t, result.CreatedPhysicalIndex)
	assert.Equal(t, contractID, result.Contract.EmbeddingContractID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSearchAdapterPhysicalIndexAndActivationPaths(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	indexName := "search_index"
	contract := &searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, IndexStrategy: string(domain.VectorIndexVectorHNSW),
		OperatorClass: "vector_cosine_ops", IndexedExpression: "embedding::vector(2)", PhysicalIndexName: indexName,
	}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}).AddRow(true, "compatible"))
	created, err := store.ensureSearchPhysicalIndex(context.Background(), contract)
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}).AddRow(false, "bad"))
	_, err = store.ensureSearchPhysicalIndex(context.Background(), contract)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}))
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT index_meta.indisvalid").WillReturnRows(sqlmock.NewRows([]string{"valid"}))
	mock.ExpectExec("(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
	created, err = store.ensureSearchPhysicalIndex(context.Background(), contract)
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, mock.ExpectationsWereMet())

	assert.NoError(t, store.createSearchPhysicalIndex(context.Background(), searchIndexGenerationDefinition{AnnStrategy: string(domain.VectorIndexExact)}))
	for _, generation := range []searchIndexGenerationDefinition{
		{AnnStrategy: string(domain.VectorIndexVectorHNSW), EmbeddingContractID: "bad", EmbeddingDimensions: 2, PhysicalIndexName: indexName},
		{AnnStrategy: string(domain.VectorIndexVectorHNSW), EmbeddingContractID: contractID, EmbeddingDimensions: 2001, PhysicalIndexName: indexName},
		{AnnStrategy: string(domain.VectorIndexHalfvecHNSW), EmbeddingContractID: contractID, EmbeddingDimensions: 4001, PhysicalIndexName: indexName},
		{AnnStrategy: "unknown", EmbeddingContractID: contractID, EmbeddingDimensions: 2, PhysicalIndexName: indexName},
		{AnnStrategy: string(domain.VectorIndexVectorHNSW), EmbeddingContractID: contractID, EmbeddingDimensions: 2, PhysicalIndexName: "bad-name"},
	} {
		assert.Error(t, store.createSearchPhysicalIndex(context.Background(), generation))
	}
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	generationID := "77777777-7777-7777-7777-777777777777"
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "active"))
	assert.NoError(t, store.activateSearchGeneration(context.Background(), generationID))
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "failed"))
	err = store.activateSearchGeneration(context.Background(), generationID)
	assert.ErrorIs(t, err, ErrSearchContractMismatch)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_index_generations").WillReturnResult(sqlmock.NewResult(0, 1))
	assert.NoError(t, store.markSearchGenerationFailed(context.Background(), generationID, errors.New("index failed")))
	require.NoError(t, mock.ExpectationsWereMet())
}
