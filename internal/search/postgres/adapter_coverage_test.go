package postgres

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// passthroughRLS keeps adapter tests focused on SQL and adapter behavior. The
// production RLS helper owns transaction setup and is covered by its package.
type passthroughRLS struct{}

func (passthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _ string, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

var _ storagepostgres.RLSHelper = passthroughRLS{}

func newSearchMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(postgresdriver.New(postgresdriver.Config{
		Conn: sqlDB, PreferSimpleProtocol: true,
	}), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewStore(db, passthroughRLS{}), mock
}

func searchIDs() (teamID, ownerID, sourceID, documentID, contractID, generationID string) {
	return "11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555",
		"66666666-6666-6666-6666-666666666666"
}

func activeSearchContract() *searchcontract.ActiveSearchContract {
	_, _, _, _, contractID, generationID := searchIDs()
	return &searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, SearchIndexGenerationID: generationID,
		EmbeddingDimensions: 2, EmbeddingProvider: "openai", EmbeddingModel: "model",
		DistanceMetric: string(domain.VectorDistanceCosine), VectorNormalization: "provider",
		DocumentFormatVersion: 1, QueryFormatVersion: 1, IndexGeneration: 1,
		IndexStrategy: string(domain.VectorIndexExact), ExactMaxRows: 100,
		CandidateLimit: 10, AllowExactFallback: true,
	}
}

func expectActiveContract(mock sqlmock.Sqlmock, contract *searchcontract.ActiveSearchContract) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM search_index_generations AS generation")).
		WithArgs(string(domain.VectorDistanceCosine)).
		WillReturnRows(sqlmock.NewRows([]string{
			"embedding_contract_id", "search_index_generation_id", "dimensions", "provider", "model",
			"distance_metric", "vector_normalization", "document_format_version", "query_format_version",
			"generation", "ann_strategy", "operator_class", "indexed_expression", "physical_index_name",
			"query_ef_search", "exact_max_rows", "candidate_limit", "allow_exact_fallback",
		}).AddRow(
			contract.EmbeddingContractID, contract.SearchIndexGenerationID, contract.EmbeddingDimensions,
			contract.EmbeddingProvider, contract.EmbeddingModel, contract.DistanceMetric,
			contract.VectorNormalization, contract.DocumentFormatVersion, contract.QueryFormatVersion,
			contract.IndexGeneration, contract.IndexStrategy, contract.OperatorClass,
			contract.IndexedExpression, contract.PhysicalIndexName, contract.QueryEFSearch,
			contract.ExactMaxRows, contract.CandidateLimit, contract.AllowExactFallback,
		))
}

func searchHitRows(teamID, documentID, sourceID, contractID string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"team_id", "search_document_id", "source_kind", "source_id", "source_version",
		"document_version", "embedding_contract_id", "search_state", "distance", "text_rank",
	}).AddRow(teamID, documentID, "evidence", sourceID, int64(2), int64(3), contractID, "current", 0.25, 0.75)
}

func TestSearchAdapterPureValidationAndHelpers(t *testing.T) {
	teamID, _, _, _, contractID, _ := searchIDs()
	assert.Equal(t, searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term", Limit: 10}, normalizeFullTextSearchInput(searchcontract.FullTextSearchInput{TeamID: " " + teamID + " ", Query: " term "}))
	assert.Equal(t, 100, normalizeFullTextSearchInput(searchcontract.FullTextSearchInput{Limit: 1000}).Limit)
	assert.Equal(t, 10, normalizeExactVectorSearchInput(searchcontract.ExactVectorSearchInput{}).Limit)
	assert.Equal(t, 100, normalizeExactVectorSearchInput(searchcontract.ExactVectorSearchInput{Limit: 1000}).Limit)

	require.NoError(t, validateFullTextSearchInput(searchcontract.FullTextSearchInput{TeamID: teamID, Query: "q"}))
	for _, input := range []searchcontract.FullTextSearchInput{
		{Query: "q"}, {TeamID: teamID}, {TeamID: teamID, Query: "q", SourceKind: "unknown"},
	} {
		assert.Error(t, validateFullTextSearchInput(input))
	}
	require.NoError(t, validateExactVectorSearchInput(searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{1}}))
	for _, input := range []searchcontract.ExactVectorSearchInput{
		{QueryEmbedding: []float32{1}}, {TeamID: "bad", QueryEmbedding: []float32{1}},
		{TeamID: teamID, EmbeddingContractID: "bad", QueryEmbedding: []float32{1}},
		{TeamID: teamID, SourceKind: "unknown", QueryEmbedding: []float32{1}}, {TeamID: teamID},
	} {
		assert.Error(t, validateExactVectorSearchInput(input))
	}

	assert.True(t, validSearchSourceKind("evidence"))
	assert.True(t, validSearchSourceKind("relationship"))
	assert.True(t, validSearchSourceKind("entity"))
	assert.False(t, validSearchSourceKind("value"))
	assert.Equal(t, 2, defaultProjectionFormat("relationship"))
	assert.Equal(t, 1, defaultProjectionFormat("evidence"))
	assert.Equal(t, "[1,-2,0.5]", mustVectorLiteral(t, []float32{1, -2, .5}))
	_, err := vectorLiteral([]float32{float32(math.NaN())})
	assert.Error(t, err)
	_, err = vectorLiteral([]float32{float32(math.Inf(1))})
	assert.Error(t, err)

	spec := deriveSearchGenerationSpec(contractID, searchmaintenance.EnsureActiveSearchContractInput{Provider: "openai", Model: "model", Dimensions: 2})
	assert.NotEmpty(t, searchMissingIndexCompatibility(&searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, IndexStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
	}, "CREATE INDEX"))
	assert.Empty(t, searchMissingIndexCompatibility(&searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, IndexStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
	}, "using hnsw (embedding::vector(2) vector_cosine_ops) embedding_contract_id = '"+contractID+"' embedding_dimensions = 2 search_state = 'current' embedding is not null"))
	assert.Equal(t, searchDocumentHash("text"), searchDocumentHash("text"))
	assert.NotEqual(t, searchDocumentHash("text"), searchDocumentHash("other"))

	for _, expression := range []string{"vector(2)", "halfvec(2)", "binary_quantize(embedding)::bit(2)", "embedding"} {
		contract := &searchcontract.ActiveSearchContract{EmbeddingDimensions: 2, IndexedExpression: expression}
		if strings.Contains(expression, "binary") {
			contract.IndexStrategy = string(domain.VectorIndexBinaryHNSW)
		}
		assert.NotEmpty(t, searchIndexExpressionToken(contract))
	}
}

func mustVectorLiteral(t *testing.T, values []float32) string {
	t.Helper()
	value, err := vectorLiteral(values)
	require.NoError(t, err)
	return value
}

func TestSearchAdapterBootstrapPoliciesAndScanners(t *testing.T) {
	input := normalizeEnsureActiveSearchContractInput(searchmaintenance.EnsureActiveSearchContractInput{Model: " model ", Dimensions: 2})
	assert.Equal(t, "openai", input.Provider)
	assert.Equal(t, "provider", input.VectorNormalization)
	assert.Equal(t, 10000, input.ExactMaxRows)
	assert.Equal(t, 200, input.CandidateLimit)
	assert.NoError(t, validateEnsureActiveSearchContractInput(input))
	for _, invalid := range []searchmaintenance.EnsureActiveSearchContractInput{
		{Model: "model", Dimensions: 0}, {Model: "model", Dimensions: domain.MaxEmbeddingDimensions + 1},
		{Provider: "openai", Dimensions: 2}, {Model: "model", Dimensions: 2, VectorNormalization: "bad"},
	} {
		assert.Error(t, validateEnsureActiveSearchContractInput(normalizeEnsureActiveSearchContractInput(invalid)))
	}

	contractID := "00112233-4455-6677-8899-aabbccddeeff"
	for _, dimensions := range []int{1536, 3072, 4096} {
		spec := deriveSearchGenerationSpec(contractID, searchmaintenance.EnsureActiveSearchContractInput{Model: "model", Dimensions: dimensions})
		assert.NoError(t, validateSearchGenerationMatchesSpec(spec, spec))
		assert.True(t, searchPhysicalIndexNameMatchesSpec(spec.PhysicalIndexName, spec) || spec.PhysicalIndexName == "")
	}
	assert.Equal(t, "search_001122334455_2_vector_hnsw_idx", derivedSearchIndexName(contractID, 2, string(domain.VectorIndexVectorHNSW)))
	assert.Equal(t, "openai:model:2:cosine:provider:doc1:query1", embeddingContractKey(searchmaintenance.EnsureActiveSearchContractInput{Provider: "openai", Model: "model", Dimensions: 2, VectorNormalization: "provider", DocumentFormatVersion: 1, QueryFormatVersion: 1}))
	for _, name := range []string{"", strings.Repeat("a", 64), "Upper", "bad-name", "1bad"} {
		_, err := validateSearchIndexName(name)
		assert.Error(t, err)
	}
	assert.Equal(t, "search_ok_1", mustIndexName(t, " search_ok_1 "))

	store, mock := newSearchMockStore(t)
	rows := sqlmock.NewRows([]string{"id", "key", "version", "provider", "model", "dimensions", "distance", "normalization", "doc", "query"}).AddRow(contractID, "key", 1, "openai", "model", 2, "cosine", "provider", 1, 1)
	mock.ExpectQuery(regexp.QuoteMeta("FROM embedding_contracts")).WillReturnRows(rows)
	db, err := store.db.Raw("SELECT * FROM embedding_contracts").Rows()
	require.NoError(t, err)
	require.True(t, db.Next())
	definition, err := scanEmbeddingContractDefinition(db)
	require.NoError(t, err)
	assert.Equal(t, contractID, definition.EmbeddingContractID)
	require.NoError(t, db.Close())

	mock.ExpectQuery(regexp.QuoteMeta("scan generation")).WillReturnRows(sqlmock.NewRows([]string{
		"id", "generation", "contract", "dimensions", "strategy", "operator", "expression", "index", "m", "ef", "query", "exact", "candidate", "fallback", "state",
	}).AddRow("generation", 1, contractID, 2, "exact", "", "", "", 16, 64, 40, 100, 10, true, "active"))
	db, err = store.db.Raw("scan generation").Rows()
	require.NoError(t, err)
	require.True(t, db.Next())
	generation, err := scanSearchGenerationDefinition(db)
	require.NoError(t, err)
	assert.Equal(t, "generation", generation.SearchIndexGenerationID)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func mustIndexName(t *testing.T, value string) string {
	t.Helper()
	name, err := validateSearchIndexName(value)
	require.NoError(t, err)
	return name
}

func TestSearchAdapterConvergenceAndDocumentPolicies(t *testing.T) {
	assert.NoError(t, validateSearchConvergenceInput(searchmaintenance.SearchConvergenceInput{}))
	assert.Error(t, validateSearchConvergenceInput(searchmaintenance.SearchConvergenceInput{EmbeddingContractID: "bad"}))
	assert.Error(t, validateSearchConvergenceInput(searchmaintenance.SearchConvergenceInput{EmbeddingDimensions: -1}))
	assert.Equal(t, "00112233-4455-6677-8899-aabbccddeeff", normalizeSearchConvergenceInput(searchmaintenance.SearchConvergenceInput{EmbeddingContractID: " 00112233-4455-6677-8899-aabbccddeeff "}).EmbeddingContractID)

	base := searchmaintenance.SearchDocumentForEmbedding{
		SearchDocumentResult: searchmaintenance.SearchDocumentResult{SourceVersion: 1, ProjectionFormat: 1, ProjectionGenerationID: " gen ", SpaceID: " space ", SpaceGeneration: 2},
		DocumentText:         "text", DocumentHash: "hash",
	}
	assert.True(t, searchDocumentMatchesCanonical(base, base))
	base.DocumentHash = "other"
	assert.False(t, searchDocumentMatchesCanonical(base, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{SourceVersion: 1, ProjectionFormat: 1, ProjectionGenerationID: "gen", SpaceID: "space", SpaceGeneration: 2}, DocumentText: "text", DocumentHash: "hash"}))
	assert.Nil(t, searchDocumentResultFromKnowledge(nil))
	assert.NotNil(t, searchTimePointer(time.Now()))

	store, mock := newSearchMockStore(t)
	teamID, _, sourceID, _, _, _ := searchIDs()
	// Unknown source kinds are deliberately outside canonical semantic ownership.
	expected, known, err := canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{SourceKind: "other"}})
	require.NoError(t, err)
	assert.False(t, known)
	assert.Nil(t, expected)

	mock.ExpectQuery(regexp.QuoteMeta("FROM evidence_fragments AS fragment")).WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(" text ", "space", int64(3)))
	expected, known, err = canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: "22222222-2222-2222-2222-222222222222", SourceID: sourceID, SourceKind: "evidence"}})
	require.NoError(t, err)
	require.True(t, known)
	require.NotNil(t, expected)
	assert.Equal(t, "text", expected.DocumentText)
	assert.Equal(t, int64(3), expected.SpaceGeneration)

	mock.ExpectQuery(regexp.QuoteMeta("FROM evidence_fragments AS fragment")).WillReturnError(sql.ErrNoRows)
	expected, known, err = canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: "22222222-2222-2222-2222-222222222222", SourceID: sourceID, SourceKind: "evidence"}})
	require.NoError(t, err)
	assert.True(t, known)
	assert.Nil(t, expected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSearchAdapterStoreNilAndValidationFailures(t *testing.T) {
	var nilStore *Store
	_, err := nilStore.GetActiveSearchContract(context.Background())
	assert.Error(t, err)
	assert.Error(t, (&Store{}).withTeamTx(context.Background(), "team", func(*gorm.DB) error { return nil }))
	assert.Error(t, (&Store{db: &gorm.DB{}}).withTeamTx(context.Background(), "team", func(*gorm.DB) error { return nil }))
	assert.Error(t, (&Store{}).withSystemTx(context.Background(), func(*gorm.DB) error { return nil }))
	assert.Error(t, (&Store{db: &gorm.DB{}}).withSystemTx(context.Background(), func(*gorm.DB) error { return nil }))

	for _, input := range []searchmaintenance.SearchReconciliationRunInput{
		{EmbeddingContractID: "bad", EmbeddingDimensions: 2},
		{EmbeddingContractID: "11111111-1111-1111-1111-111111111111"},
		{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, StaleAfter: 25 * time.Hour},
	} {
		_, _, err := (&Store{}).ReserveSearchReconciliationRun(context.Background(), input)
		assert.Error(t, err)
	}
	_, err = (&Store{}).SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{RunID: "bad", EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2})
	assert.Error(t, err)
	_, err = (&Store{}).SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: "bad", EmbeddingDimensions: 2})
	assert.Error(t, err)
	_, err = (&Store{}).CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: "bad", EmbeddingDimensions: 2})
	assert.Error(t, err)
	err = (&Store{}).FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: "bad", Status: "completed"})
	assert.Error(t, err)
}

func TestSearchAdapterActiveContractReadAndErrors(t *testing.T) {
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	got, err := store.GetActiveSearchContract(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, contract.EmbeddingContractID, got.EmbeddingContractID)
	assert.Equal(t, contract.IndexStrategy, got.IndexStrategy)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM search_index_generations AS generation")).
		WithArgs(string(domain.VectorDistanceCosine)).
		WillReturnRows(sqlmock.NewRows([]string{
			"embedding_contract_id", "search_index_generation_id", "dimensions", "provider", "model",
			"distance_metric", "vector_normalization", "document_format_version", "query_format_version",
			"generation", "ann_strategy", "operator_class", "indexed_expression", "physical_index_name",
			"query_ef_search", "exact_max_rows", "candidate_limit", "allow_exact_fallback",
		}))
	_, err = store.GetActiveSearchContract(context.Background())
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM search_index_generations AS generation")).
		WithArgs(string(domain.VectorDistanceCosine)).WillReturnError(errors.New("query failed"))
	_, err = store.GetActiveSearchContract(context.Background())
	assert.Contains(t, err.Error(), "load active contract")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("FROM search_index_generations AS generation")).
		WithArgs(string(domain.VectorDistanceCosine)).WillReturnRows(sqlmock.NewRows([]string{
		"embedding_contract_id", "search_index_generation_id", "dimensions", "provider", "model",
		"distance_metric", "vector_normalization", "document_format_version", "query_format_version",
		"generation", "ann_strategy", "operator_class", "indexed_expression", "physical_index_name",
		"query_ef_search", "exact_max_rows", "candidate_limit", "allow_exact_fallback",
	}).AddRow("", "", 2, "openai", "model", "cosine", "provider", 1, 1, 1, "exact", "", "", "", 40, 100, 10, true))
	_, err = store.GetActiveSearchContract(context.Background())
	require.ErrorIs(t, err, ErrSearchContractMismatch)
}

func TestSearchAdapterFullTextReadAndErrors(t *testing.T) {
	teamID, _, sourceID, documentID, contractID, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH.*FROM recall_relationship_generation").
		WillReturnRows(searchHitRows(teamID, documentID, sourceID, contractID))
	hits, err := store.SearchFullText(context.Background(), searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term", Limit: 1})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, documentID, hits[0].SearchDocumentID)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH.*FROM recall_relationship_generation").
		WillReturnRows(searchHitRows(teamID, documentID, sourceID, contractID))
	hits, err = store.SearchFullText(context.Background(), searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term", SourceKind: "evidence"})
	require.NoError(t, err)
	require.Len(t, hits, 1)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH.*FROM recall_relationship_generation").WillReturnError(errors.New("search failed"))
	_, err = store.SearchFullText(context.Background(), searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term"})
	assert.Contains(t, err.Error(), "full-text search")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH.*FROM recall_relationship_generation").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "search_document_id", "source_kind", "source_id", "source_version",
		"document_version", "embedding_contract_id", "search_state", "distance", "text_rank",
	}).AddRow(teamID, documentID, "evidence", sourceID, "bad", 3, contractID, "current", .2, .3))
	_, err = store.SearchFullText(context.Background(), searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term"})
	assert.Contains(t, err.Error(), "full-text search")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH.*FROM recall_relationship_generation").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "search_document_id", "source_kind", "source_id", "source_version",
		"document_version", "embedding_contract_id", "search_state", "distance", "text_rank",
	}).AddRow(teamID, documentID, "evidence", sourceID, int64(2), int64(3), contractID, "current", .2, .3).RowError(0, errors.New("row failed")))
	_, err = store.SearchFullText(context.Background(), searchcontract.FullTextSearchInput{TeamID: teamID, Query: "term"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full-text search")
}

func TestSearchAdapterExactVectorReadAndFences(t *testing.T) {
	teamID, _, sourceID, documentID, contractID, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	contract := activeSearchContract()
	expectActiveContract(mock, contract)
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM \(`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT team_id::text, search_document_id::text").WillReturnRows(searchHitRows(teamID, documentID, sourceID, contractID))
	hits, err := store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25, .75}, Limit: 1})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, documentID, hits[0].SearchDocumentID)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM \(`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT team_id::text, search_document_id::text").WillReturnRows(searchHitRows(teamID, documentID, sourceID, contractID))
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25, .75}, SourceKind: "evidence"})
	require.NoError(t, err)

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM \(`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(101))
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25, .75}})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	// Contract and vector fences fail before any document query.
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, EmbeddingContractID: "77777777-7777-7777-7777-777777777777", QueryEmbedding: []float32{.25, .75}})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25}})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{float32(math.NaN()), .75}})
	assert.Error(t, err)

	contract.AllowExactFallback = false
	contract.IndexStrategy = string(domain.VectorIndexVectorHNSW)
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25, .75}})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	contract = activeSearchContract()
	contract.DistanceMetric = "euclidean"
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{.25, .75}})
	require.ErrorIs(t, err, ErrSearchContractMismatch)
}

func TestSearchAdapterReadinessClassifications(t *testing.T) {
	_, _, _, _, _, _ = searchIDs()
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM pg_extension")).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("(?s)WITH activated_generation.*SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	readiness, err := store.CheckSearchReadiness(context.Background())
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	assert.Len(t, readiness.Reasons, 2)

	contract = activeSearchContract()
	contract.IndexStrategy = string(domain.VectorIndexVectorHNSW)
	contract.PhysicalIndexName = "search_index"
	contract.OperatorClass = "vector_cosine_ops"
	contract.IndexedExpression = "embedding::vector(2)"
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM pg_extension")).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}))
	mock.ExpectQuery("(?s)WITH activated_generation.*SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	readiness, err = store.CheckSearchReadiness(context.Background())
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	assert.Contains(t, readiness.Reasons[0].Code, "missing_physical_index")

	for _, valid := range []bool{false, true} {
		store, mock = newSearchMockStore(t)
		expectActiveContract(mock, contract)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM pg_extension")).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		definition := "using hnsw (embedding::vector(2) vector_cosine_ops) embedding_contract_id = '" + contract.EmbeddingContractID + "' embedding_dimensions = 2 search_state = 'current' embedding is not null"
		if valid {
			definition = "wrong definition"
		}
		mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}).AddRow(valid, definition))
		mock.ExpectQuery("(?s)WITH activated_generation.*SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		readiness, err = store.CheckSearchReadiness(context.Background())
		require.NoError(t, err)
		require.False(t, readiness.Ready)
	}
}

func TestSearchAdapterContractForVectorSearchErrors(t *testing.T) {
	teamID, _, _, _, _, _ := searchIDs()
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err := store.contractForVectorSearch(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, EmbeddingContractID: "77777777-7777-7777-7777-777777777777"})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	contract.DistanceMetric = "euclidean"
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.contractForVectorSearch(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID})
	require.ErrorIs(t, err, ErrSearchContractMismatch)
}

func bootstrapInput(dimensions int) searchmaintenance.EnsureActiveSearchContractInput {
	return searchmaintenance.EnsureActiveSearchContractInput{
		Provider: "openai", Model: "model", Dimensions: dimensions,
		VectorNormalization: "provider", DocumentFormatVersion: 1, QueryFormatVersion: 1,
		ExactMaxRows: 100, CandidateLimit: 10,
	}
}

func embeddingContractRows(contractID string, input searchmaintenance.EnsureActiveSearchContractInput, count int) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"embedding_contract_id", "contract_key", "version", "provider", "model", "dimensions",
		"distance_metric", "vector_normalization", "document_format_version", "query_format_version",
	})
	if count > 0 {
		for i := 0; i < count; i++ {
			rows.AddRow(contractID, "key", i+1, input.Provider, input.Model, input.Dimensions, "cosine", input.VectorNormalization, input.DocumentFormatVersion, input.QueryFormatVersion)
		}
	}
	return rows
}

func searchGenerationRows(generation SearchIndexGenerationDefinition, count int) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "generation", "contract", "dimensions", "strategy", "operator", "expression", "index",
		"m", "ef", "query", "exact", "candidate", "fallback", "state",
	})
	if count > 0 {
		for i := 0; i < count; i++ {
			rows.AddRow(generation.SearchIndexGenerationID, generation.Generation, generation.EmbeddingContractID,
				generation.EmbeddingDimensions, generation.AnnStrategy, generation.OperatorClass,
				generation.IndexedExpression, generation.PhysicalIndexName, generation.HNSWM,
				generation.HNSWEFConstruction, generation.QueryEFSearch, generation.ExactMaxRows,
				generation.CandidateLimit, generation.AllowExactFallback, generation.ActivationState)
		}
	}
	return rows
}

func TestSearchAdapterEnsureEmbeddingContractBranches(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	input := bootstrapInput(2)
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 1))
	got, created, err := ensureEmbeddingContractInTx(context.Background(), store.db, input)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, contractID, got.EmbeddingContractID)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 2))
	_, _, err = ensureEmbeddingContractInTx(context.Background(), store.db, input)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 1))
	_, _, err = ensureEmbeddingContractInTx(context.Background(), store.db, bootstrapInput(3))
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM embedding_contracts")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("(?s)INSERT INTO embedding_contracts").WillReturnResult(sqlmock.NewResult(0, 1))
	got, created, err = ensureEmbeddingContractInTx(context.Background(), store.db, input)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, input.Model, got.Model)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM embedding_contracts")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	_, _, err = ensureEmbeddingContractInTx(context.Background(), store.db, input)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnError(errors.New("contract query failed"))
	_, _, err = ensureEmbeddingContractInTx(context.Background(), store.db, input)
	assert.Contains(t, err.Error(), "list active embedding contracts")
}

func TestSearchAdapterEnsureGenerationBranches(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	input := bootstrapInput(2)
	contract := embeddingContractDefinition{EmbeddingContractID: contractID, Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions, DistanceMetric: string(domain.VectorDistanceCosine), VectorNormalization: input.VectorNormalization, DocumentFormatVersion: 1, QueryFormatVersion: 1}
	spec := deriveSearchGenerationSpec(contractID, input)

	store, mock := newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: "77777777-7777-7777-7777-777777777777", Generation: 1,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, AnnStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, HNSWM: 16, HNSWEFConstruction: 64,
		QueryEFSearch: spec.QueryEFSearch, ExactMaxRows: 100, CandidateLimit: 10,
		AllowExactFallback: spec.AllowExactFallback, ActivationState: "active",
	}, 1))
	got, created, err := ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, string(domain.SearchIndexGenerationActive), got.ActivationState)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnError(errors.New("lock failed"))
	_, _, err = ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	assert.Contains(t, err.Error(), "lock search generation")

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: "77777777-7777-7777-7777-777777777777", Generation: 1,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, AnnStrategy: "bad", ActivationState: "building",
	}, 1))
	_, _, err = ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: "77777777-7777-7777-7777-777777777777", Generation: 2,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, AnnStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, HNSWM: 16, HNSWEFConstruction: 64,
		QueryEFSearch: spec.QueryEFSearch, ExactMaxRows: 100, CandidateLimit: 10,
		AllowExactFallback: spec.AllowExactFallback, ActivationState: "building",
	}, 1))
	got, created, err = ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "building", got.ActivationState)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: "77777777-7777-7777-7777-777777777777", Generation: 1,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, AnnStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, HNSWM: 16, HNSWEFConstruction: 64,
		QueryEFSearch: spec.QueryEFSearch, ExactMaxRows: 100, CandidateLimit: 10,
		AllowExactFallback: spec.AllowExactFallback, ActivationState: "deprecated",
	}, 1))
	mock.ExpectExec("(?s)INSERT INTO search_index_generations").WillReturnResult(sqlmock.NewResult(0, 1))
	got, created, err = ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 2, got.Generation)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: "77777777-7777-7777-7777-777777777777", Generation: 1,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, AnnStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, HNSWM: 16, HNSWEFConstruction: 64,
		QueryEFSearch: spec.QueryEFSearch, ExactMaxRows: 100, CandidateLimit: 10,
		AllowExactFallback: spec.AllowExactFallback, ActivationState: "active",
	}, 2))
	_, _, err = ensureSearchGenerationInTx(context.Background(), store.db, contract, input)
	require.ErrorIs(t, err, ErrSearchContractMismatch)
}
