package postgres

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
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

	store, mock = newSearchMockStore(t)
	generationID := "77777777-7777-7777-7777-777777777777"
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "active"))
	assert.NoError(t, store.activateSearchGeneration(context.Background(), generationID))

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "failed"))
	err = store.activateSearchGeneration(context.Background(), generationID)
	assert.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_index_generations").WillReturnResult(sqlmock.NewResult(0, 1))
	assert.NoError(t, store.markSearchGenerationFailed(context.Background(), generationID, errors.New("index failed")))
}

func reconciliationDocumentRows(document searchmaintenance.SearchDocumentForEmbedding, vectorCurrent bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"team_id", "search_document_id", "owner_profile_id", "source_kind", "source_id", "source_version",
		"projection_format_version", "projection_generation_id", "document_version", "embedding_contract_id",
		"embedding_dimensions", "search_state", "space_id", "space_generation", "document_text", "document_hash", "vector_current",
	}).AddRow(document.TeamID, document.SearchDocumentID, document.OwnerProfileID, document.SourceKind, document.SourceID,
		document.SourceVersion, document.ProjectionFormat, document.ProjectionGenerationID, document.DocumentVersion,
		document.EmbeddingContractID, document.EmbeddingDimensions, document.SearchState, document.SpaceID,
		document.SpaceGeneration, document.DocumentText, document.DocumentHash, vectorCurrent)
}

func reconciliationStoredDocumentRows(document searchmaintenance.SearchDocumentForEmbedding) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"team_id", "search_document_id", "owner_profile_id", "source_kind", "source_id", "source_version",
		"projection_format_version", "projection_generation_id", "document_version", "embedding_contract_id",
		"embedding_dimensions", "search_state", "space_id", "space_generation", "document_text", "document_hash",
	}).AddRow(document.TeamID, document.SearchDocumentID, document.OwnerProfileID, document.SourceKind, document.SourceID,
		document.SourceVersion, document.ProjectionFormat, document.ProjectionGenerationID, document.DocumentVersion,
		document.EmbeddingContractID, document.EmbeddingDimensions, document.SearchState, document.SpaceID,
		document.SpaceGeneration, document.DocumentText, document.DocumentHash)
}

func TestSearchAdapterReserveReconciliationRunBranches(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT reconciliation_run_id::text").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	started := now.Add(time.Minute)
	mock.ExpectQuery("(?s)INSERT INTO search_reconciliation_runs").WillReturnRows(sqlmock.NewRows([]string{
		"run_id", "local_date", "status", "selected", "embedded", "updated", "drifted", "error", "started", "completed", "updated_at",
	}).AddRow("77777777-7777-7777-7777-777777777777", now, "running", 0, 0, 0, 0, "", started, nil, now))
	run, claimed, err := store.ReserveSearchReconciliationRun(context.Background(), searchmaintenance.SearchReconciliationRunInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: now})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	assert.Equal(t, "77777777-7777-7777-7777-777777777777", run.RunID)
	assert.NotNil(t, run.StartedAt)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	run, claimed, err = store.ReserveSearchReconciliationRun(context.Background(), searchmaintenance.SearchReconciliationRunInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: now})
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Nil(t, run)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT reconciliation_run_id::text").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("88888888-8888-8888-8888-888888888888"))
	run, claimed, err = store.ReserveSearchReconciliationRun(context.Background(), searchmaintenance.SearchReconciliationRunInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: now})
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Nil(t, run)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnError(errors.New("lock query failed"))
	_, _, err = store.ReserveSearchReconciliationRun(context.Background(), searchmaintenance.SearchReconciliationRunInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: now})
	assert.Contains(t, err.Error(), "reserve reconciliation run")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnError(errors.New("expire failed"))
	_, _, err = store.ReserveSearchReconciliationRun(context.Background(), searchmaintenance.SearchReconciliationRunInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Now: now})
	assert.Contains(t, err.Error(), "reserve reconciliation run")
}

func TestSearchAdapterSelectReconciliationDocumentsBranches(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	base := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "pending",
	}, DocumentText: "text", DocumentHash: "hash"}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationDocumentRows(base, false))
	result, err := store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, documentID, result[0].SearchDocumentID)
	require.NoError(t, mock.ExpectationsWereMet())

	runID := "77777777-7777-7777-7777-777777777777"
	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)SELECT selection_cursor_team_id::text").WillReturnRows(sqlmock.NewRows([]string{"team", "kind", "source", "document"}).AddRow(teamID, "other", sourceID, documentID))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationDocumentRows(base, false))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	result, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{RunID: runID, EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	require.NoError(t, err)
	require.Len(t, result, 1)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "search_document_id", "owner_profile_id", "source_kind", "source_id", "source_version", "projection_format_version", "projection_generation_id", "document_version", "embedding_contract_id", "embedding_dimensions", "search_state", "space_id", "space_generation", "document_text", "document_hash", "vector_current",
	}))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "owner_profile_id", "source_id", "text", "space_id", "space_generation",
	}).AddRow(teamID, ownerID, sourceID, " evidence ", "", int64(0)))
	result, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "evidence", result[0].SourceKind)
	assert.Equal(t, searchDocumentHash("evidence"), result[0].DocumentHash)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)SELECT selection_cursor_team_id::text").WillReturnRows(sqlmock.NewRows([]string{"team", "kind", "source", "document"}))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnError(errors.New("selection failed"))
	_, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{RunID: runID, EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	assert.Contains(t, err.Error(), "select reconciliation documents")
}

func TestSearchAdapterCompleteReconciliationDocumentsBranches(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	document := searchmaintenance.SearchDocumentEmbedding{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentHash: "hash", StoredDocumentHash: "hash",
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Embedding: []float32{.1, .9}, DocumentText: "text",
	}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "pending",
	}, DocumentText: "text", DocumentHash: "hash"}))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 1))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{document}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.UpdatedCount)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "search_document_id", "owner_profile_id", "source_kind", "source_id", "source_version", "projection_format_version", "projection_generation_id", "document_version", "embedding_contract_id", "embedding_dimensions", "search_state", "space_id", "space_generation", "document_text", "document_hash",
	}))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{document}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	retired := document
	retired.Retired = true
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{retired}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	retired.SourceKind = "evidence"
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 1))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{retired}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.UpdatedCount)

	for _, invalid := range []searchmaintenance.ApplySearchReconciliationInput{
		{EmbeddingContractID: "bad", EmbeddingDimensions: 2},
		{EmbeddingContractID: contractID, EmbeddingDimensions: 0},
		{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{{Retired: true}}},
		{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{{TeamID: "bad", OwnerProfileID: ownerID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: "hash"}}},
	} {
		_, err := store.CompleteSearchReconciliationDocuments(context.Background(), invalid)
		assert.Error(t, err)
	}
}

func TestSearchAdapterConvergenceProjection(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT count\(DISTINCT document.team_id\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"class", "count"}).AddRow("vector_missing", 1))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(EXTRACT`).WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(30.0))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "search_document_id", "owner_profile_id", "source_kind", "source_id", "source_version",
		"projection_format_version", "projection_generation_id", "document_version", "embedding_contract_id", "embedding_dimensions", "search_state", "space_id", "space_generation", "document_text", "document_hash", "vector_current", "updated_at",
	}).AddRow(teamID, documentID, ownerID, "other", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending", "", int64(0), "text", "hash", false, time.Now().UTC()))
	mock.ExpectQuery("(?s)WITH canonical_sources AS").WillReturnRows(sqlmock.NewRows([]string{"team", "count", "seconds"}))
	mock.ExpectQuery("(?s)FROM search_reconciliation_runs").WillReturnRows(sqlmock.NewRows([]string{
		"run_id", "local_date", "status", "selected", "embedded", "updated", "drifted", "error", "started", "completed", "updated_at",
	}))
	result, err := store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "attention_required", result.Status)
	assert.EqualValues(t, 1, result.ExpectedDocuments)
	assert.EqualValues(t, 1, result.DriftedDocuments)
	assert.Equal(t, "state_not_current", result.DriftClasses[0].Class)
	require.NoError(t, mock.ExpectationsWereMet())

	// A query failure in the first projection is classified at the adapter boundary.
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnError(errors.New("convergence query failed"))
	_, err = store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{})
	assert.Contains(t, err.Error(), "convergence projection")
}

func TestSearchAdapterFinishReconciliationRunBranches(t *testing.T) {
	runID := "77777777-7777-7777-7777-777777777777"
	store, mock := newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	err := store.FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: runID, Status: "completed", SelectedCount: 1, EmbeddedCount: 1, UpdatedCount: 1, DriftedCount: 0})
	require.NoError(t, err)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	err = store.FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: runID, Status: "failed", SelectedCount: 1, LastError: strings.Repeat("x", 300)})
	require.NoError(t, err)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 0))
	err = store.FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: runID, Status: "completed"})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnError(errors.New("finish failed"))
	err = store.FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: runID, Status: "completed"})
	assert.Contains(t, err.Error(), "finish reconciliation run")

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnError(errors.New("reset failed"))
	err = store.FinishSearchReconciliationRun(context.Background(), searchmaintenance.FinishSearchReconciliationRunInput{RunID: runID, Status: "failed"})
	assert.Contains(t, err.Error(), "finish reconciliation run")
	_ = mock
}

func TestSearchAdapterExportedCompatibilitySeams(t *testing.T) {
	input := searchmaintenance.EnsureActiveSearchContractInput{Model: "model", Dimensions: 2}
	assert.Equal(t, "openai", NormalizeEnsureActiveSearchContractInput(input).Provider)
	assert.NoError(t, ValidateEnsureActiveSearchContractInput(NormalizeEnsureActiveSearchContractInput(input)))
	spec := DeriveSearchGenerationSpec("11111111-1111-1111-1111-111111111111", NormalizeEnsureActiveSearchContractInput(input))
	assert.NoError(t, ValidateSearchGenerationMatchesSpec(spec, spec))
	assert.True(t, SearchPhysicalIndexNameMatchesSpec(spec.PhysicalIndexName, spec) || spec.PhysicalIndexName == "")
	assert.NotEmpty(t, DerivedSearchIndexName("11111111-1111-1111-1111-111111111111", 2, string(domain.VectorIndexVectorHNSW)))
	assert.NotEmpty(t, EmbeddingContractKey(NormalizeEnsureActiveSearchContractInput(input)))
	assert.Equal(t, input.Model, NormalizeEnsureActiveSearchContractInput(input).Model)
	assert.NoError(t, ValidateActiveContractMatchesConfig(&searchcontract.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2,
		EmbeddingProvider: "openai", EmbeddingModel: "model", DistanceMetric: string(domain.VectorDistanceCosine),
		VectorNormalization: "provider", DocumentFormatVersion: 1, QueryFormatVersion: 1,
		IndexStrategy: spec.AnnStrategy, OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName,
	}, NormalizeEnsureActiveSearchContractInput(input)))
	assert.NoError(t, ValidateFullTextSearchInput(searchcontract.FullTextSearchInput{TeamID: searchIDsTeam(), Query: "q"}))
	assert.NoError(t, ValidateExactVectorSearchInput(searchcontract.ExactVectorSearchInput{TeamID: searchIDsTeam(), QueryEmbedding: []float32{1}}))
	assert.Equal(t, 1, DefaultProjectionFormat("evidence"))
	assert.True(t, ValidSearchSourceKind("entity"))
	assert.Equal(t, "[1]", mustVectorLiteral(t, []float32{1}))
	assert.NotEmpty(t, SearchMissingIndexCompatibility(&searchcontract.ActiveSearchContract{}, ""))
	assert.True(t, SearchIndexExpressionCompatible(&searchcontract.ActiveSearchContract{}, ""))
	_, err := ScanSearchHit(scanErrorStub{})
	assert.Error(t, err)
}

func searchIDsTeam() string { teamID, _, _, _, _, _ := searchIDs(); return teamID }

type scanErrorStub struct{}

func (scanErrorStub) Scan(...any) error { return errors.New("scan failed") }

func TestSearchAdapterRelationshipAndMissingDocumentHelpers(t *testing.T) {
	teamID, ownerID, sourceID, _, contractID, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{
		"team", "relationship", "owner", "space", "space_generation", "group", "subject", "predicate", "predicate_version", "object_entity", "object_value", "kind", "cardinality", "status", "polarity", "scope", "valid_from", "valid_to", "alias", "support_count", "source_group_count", "version",
	}).AddRow(teamID, sourceID, ownerID, "", int64(0), "group", "subject", "predicate", 1, "", "", "entity", 1, "retracted", "+", "", nil, nil, "", 0, 0, 1))
	expected, known, err := canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: ownerID, SourceID: sourceID, SourceKind: "relationship"}})
	require.NoError(t, err)
	assert.True(t, known)
	assert.Nil(t, expected)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	seed := []searchmaintenance.SearchDocumentForEmbedding{}
	contract := &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2}
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source", "text", "space", "generation"}))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source"}).AddRow(teamID, ownerID, sourceID))
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	err = selectMissingCanonicalSearchDocuments(context.Background(), store.db, contract, 2, &seed)
	require.NoError(t, err)
	assert.Empty(t, seed)

	assert.False(t, searchDocumentMatchesCanonical(searchmaintenance.SearchDocumentForEmbedding{}, searchmaintenance.SearchDocumentForEmbedding{DocumentHash: "hash"}))
	_, err = completeMissingCanonicalSearchDocument(context.Background(), store.db, contract, searchmaintenance.SearchDocumentEmbedding{SourceKind: "", SourceID: sourceID})
	assert.Error(t, err)
	_, err = completeMissingCanonicalSearchDocument(context.Background(), store.db, contract, searchmaintenance.SearchDocumentEmbedding{SourceKind: "evidence", SourceID: "bad"})
	assert.Error(t, err)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("canonical", "", int64(0)))
	updated, err := completeMissingCanonicalSearchDocument(context.Background(), store.db, contract, searchmaintenance.SearchDocumentEmbedding{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentText: "wrong", DocumentHash: "wrong",
		Embedding: []float32{1, 2}, SpaceGeneration: 0,
	})
	require.NoError(t, err)
	assert.False(t, updated)

	value := searchDocumentResultFromKnowledge(&knowledgecontract.SearchDocumentResult{TeamID: teamID, SearchDocumentID: "77777777-7777-7777-7777-777777777777"})
	assert.NotNil(t, value)
}

func TestSearchAdapterUpsertSearchDocumentStaleFence(t *testing.T) {
	teamID, ownerID, sourceID, _, contractID, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state",
	}))
	result, err := upsertSearchDocumentInTx(context.Background(), store.db, searchmaintenance.UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "text", DocumentHash: "hash", SpaceGeneration: 0,
	}, &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2})
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSearchAdapterRefreshProjectionGeneration(t *testing.T) {
	store, mock := newSearchMockStore(t)
	mock.ExpectExec("UPDATE search_projection_generations").WillReturnResult(sqlmock.NewResult(0, 1))
	err := refreshRelationshipProjectionGeneration(context.Background(), store.db, searchIDsTeam(), "77777777-7777-7777-7777-777777777777")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
