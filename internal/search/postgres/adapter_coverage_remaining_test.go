package postgres

import (
	"context"
	"errors"
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
)

func TestSearchAdapterCompleteMissingCanonicalDocument(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	text := "canonical text"
	hash := searchDocumentHash(text)
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(text, "", int64(0)))
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state",
	}).AddRow(teamID, documentID, ownerID, "", int64(0), "evidence", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending"))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 1))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
		Documents: []searchmaintenance.SearchDocumentEmbedding{{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
			SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentText: text, DocumentHash: hash,
			EmbeddingContractID: contractID, EmbeddingDimensions: 2, Embedding: []float32{.2, .8}, SpaceGeneration: 0,
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.EqualValues(t, 1, result.UpdatedCount)
	require.NoError(t, mock.ExpectationsWereMet())

	// A canonical writer may win between selection and the provider response.
	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(text, "", int64(0)))
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state",
	}))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{
		EmbeddingContractID: contractID, EmbeddingDimensions: 2,
		Documents: []searchmaintenance.SearchDocumentEmbedding{{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
			SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentText: text, DocumentHash: hash,
			EmbeddingContractID: contractID, EmbeddingDimensions: 2, Embedding: []float32{.2, .8}, SpaceGeneration: 0,
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)
}

func TestSearchAdapterCompleteFencedCurrentDocumentOutcomes(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	text := "text"
	hash := searchDocumentHash(text)
	input := searchmaintenance.SearchDocumentEmbedding{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentText: text, DocumentHash: hash, StoredDocumentHash: hash,
		EmbeddingContractID: contractID, EmbeddingDimensions: 2, Embedding: []float32{.2, .8}, SpaceGeneration: 0,
	}
	current := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "pending",
	}, DocumentText: text, DocumentHash: hash}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(current))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(text, "", int64(0)))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 1))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.UpdatedCount)

	store, mock = newSearchMockStore(t)
	current.SearchState = "current"
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(current))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(text, "", int64(0)))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(current))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("changed", "", int64(0)))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	for _, invalid := range []searchmaintenance.SearchDocumentEmbedding{
		{TeamID: teamID, SearchDocumentID: "bad", OwnerProfileID: ownerID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: hash},
		{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: "bad", SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: hash},
		{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceVersion: 0, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: hash},
	} {
		_, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{invalid}})
		assert.Error(t, err)
	}
}

func TestSearchAdapterCanonicalConvergenceBranches(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	contract := &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2}
	stored := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "current",
	}, DocumentText: "text", DocumentHash: searchDocumentHash("text")}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "kind", "source", "source_version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state", "space", "space_generation", "text", "hash", "vector_current", "updated_at",
	}).AddRow(teamID, documentID, ownerID, "evidence", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending", "", int64(0), "text", stored.DocumentHash, false, time.Now().UTC()))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	mock.ExpectQuery("(?s)WITH canonical_sources AS").WillReturnRows(sqlmock.NewRows([]string{"team", "count", "seconds"}).AddRow(teamID, int64(2), 15.0))
	result, err := store.canonicalSearchConvergence(context.Background(), contract)
	require.NoError(t, err)
	assert.EqualValues(t, 2, result.ExpectedDocuments)
	assert.EqualValues(t, 3, result.DriftedDocuments)
	assert.EqualValues(t, 1, result.AffectedTeamCount)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH canonical_sources AS").WillReturnRows(sqlmock.NewRows([]string{"team", "count", "seconds"}).AddRow(teamID, int64(2), 15.0))
	stats := &canonicalSearchConvergence{}
	classes := map[string]int64{}
	teams := map[string]struct{}{}
	err = addMissingCanonicalSearchStats(context.Background(), store.db, contract, stats, classes, teams, time.Now().UTC())
	require.NoError(t, err)
	assert.EqualValues(t, 2, stats.ExpectedDocuments)
	assert.EqualValues(t, 2, classes["canonical_document_missing"])
	assert.Contains(t, teams, teamID)

	store, _ = newSearchMockStore(t)
	expected, known, err := CanonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{SourceKind: "other"}})
	require.NoError(t, err)
	assert.False(t, known)
	assert.Nil(t, expected)
	_ = stored
}

func TestSearchAdapterConvergenceValidationAndLatestRun(t *testing.T) {
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err := store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{EmbeddingContractID: "77777777-7777-7777-7777-777777777777"})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	_, err = store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{EmbeddingDimensions: 3})
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	// The latest run is optional metadata, but a present row must be scanned and
	// returned with the canonical convergence result.
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	for _, value := range []any{0, 0, 0} {
		mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(value))
	}
	mock.ExpectQuery("(?s)SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"class", "count"}))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(EXTRACT`).WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(0.0))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "kind", "source", "source_version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state", "space", "space_generation", "text", "hash", "vector_current", "updated_at",
	}))
	mock.ExpectQuery("(?s)WITH canonical_sources AS").WillReturnRows(sqlmock.NewRows([]string{"team", "count", "seconds"}))
	started := time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC)
	updated := started.Add(time.Hour)
	mock.ExpectQuery("(?s)FROM search_reconciliation_runs").WillReturnRows(sqlmock.NewRows([]string{
		"run_id", "local_date", "status", "selected", "embedded", "updated", "drifted", "error", "started", "completed", "updated_at",
	}).AddRow("77777777-7777-7777-7777-777777777777", started, "completed", 2, 2, 2, 0, "", started, updated, updated))
	result, err := store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{})
	require.NoError(t, err)
	require.NotNil(t, result.LatestRun)
	assert.Equal(t, int64(2), result.LatestRun.SelectedCount)
	assert.Equal(t, "2026-09-10", result.LatestRun.LocalRunDate.Format("2006-01-02"))
}

func TestSearchAdapterConvergenceErrorClassification(t *testing.T) {
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
	mock.ExpectQuery(`(?s)SELECT count\(DISTINCT document.team_id\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("(?s)SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"class", "count"}).AddRow("bad", "invalid"))
	_, err := store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{})
	assert.Contains(t, err.Error(), "convergence projection")

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
	mock.ExpectQuery(`(?s)SELECT count\(DISTINCT document.team_id\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("(?s)SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"class", "count"}))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(EXTRACT`).WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(0.0))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnError(errors.New("canonical query failed"))
	_, err = store.GetSearchConvergence(context.Background(), searchmaintenance.SearchConvergenceInput{})
	assert.Contains(t, err.Error(), "canonical convergence projection")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_reconciliation_runs").WillReturnError(errors.New("run query failed"))
	_, err = store.latestSearchReconciliationRun(context.Background())
	assert.Contains(t, err.Error(), "latest reconciliation run")
}

func TestSearchAdapterPhysicalIndexErrorPaths(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	generation := searchIndexGenerationDefinition{AnnStrategy: string(domain.VectorIndexVectorHNSW), EmbeddingContractID: contractID, EmbeddingDimensions: 2, PhysicalIndexName: "search_index", OperatorClass: "vector_cosine_ops"}
	store, mock := newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnError(errors.New("lock failed"))
	err := store.createSearchPhysicalIndex(context.Background(), generation)
	assert.Contains(t, err.Error(), "lock physical index")

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT index_meta.indisvalid").WillReturnError(errors.New("index query failed"))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
	err = store.createSearchPhysicalIndex(context.Background(), generation)
	assert.Contains(t, err.Error(), "check physical index")

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT index_meta.indisvalid").WillReturnRows(sqlmock.NewRows([]string{"valid"}))
	mock.ExpectExec("(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS").WillReturnError(errors.New("ddl failed"))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
	err = store.createSearchPhysicalIndex(context.Background(), generation)
	assert.Contains(t, err.Error(), "create physical index")
}

func TestSearchAdapterSystemTeamAndCheckConvergenceFailures(t *testing.T) {
	store, _ := newSearchMockStore(t)
	store.rls = nil
	err := store.withActiveSystemTeamTx(context.Background(), searchIDsTeam(), func(*gorm.DB) error { return nil })
	assert.Error(t, err)
	store.rls = passthroughRLS{}
	store.db = nil
	err = store.withActiveSystemTeamTx(context.Background(), searchIDsTeam(), func(*gorm.DB) error { return nil })
	assert.Error(t, err)

	var nilStore *Store
	err = nilStore.CheckSearchConvergence(context.Background())
	assert.Error(t, err)
}

func TestSearchAdapterActiveRelationshipProjection(t *testing.T) {
	teamID, ownerID, sourceID, _, _, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{
		"team", "relationship", "owner", "space", "space_generation", "group", "subject", "predicate", "predicate_version", "object_entity", "object_value", "kind", "cardinality", "status", "polarity", "scope", "valid_from", "valid_to", "alias", "support_count", "source_group_count", "version",
	}).AddRow(teamID, sourceID, ownerID, "", int64(0), "group", "subject", "predicate_key", 1, "object", "", "entity", 1, "active", "+", "scope", nil, nil, "", 1, 1, 2))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"subject", "object", "type", "value", "unit"}).AddRow("Subject", "Object", "", "", ""))
	mock.ExpectQuery("(?s)WITH activated AS").WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow("77777777-7777-7777-7777-777777777777"))
	expected, known, err := canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: ownerID, SourceID: sourceID, SourceKind: "relationship"}})
	require.NoError(t, err)
	require.True(t, known)
	require.NotNil(t, expected)
	assert.Contains(t, expected.DocumentText, "predicate key")
	assert.Equal(t, int64(2), expected.SourceVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSearchAdapterActivateBuildingGeneration(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	generationID := "77777777-7777-7777-7777-777777777777"
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "building"))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)SET activation_state = 'deprecated'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)SET activation_state = 'active'").WillReturnResult(sqlmock.NewResult(0, 1))
	err := store.activateSearchGeneration(context.Background(), generationID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "building"))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)SET activation_state = 'deprecated'").WillReturnError(errors.New("deprecate failed"))
	err = store.activateSearchGeneration(context.Background(), generationID)
	assert.Contains(t, err.Error(), "deprecate failed")
}

func TestSearchAdapterEnsureInputAndRLSBoundaryFailures(t *testing.T) {
	store, _ := newSearchMockStore(t)
	_, err := store.EnsureActiveSearchContract(context.Background(), searchmaintenance.EnsureActiveSearchContractInput{Model: "model", Dimensions: 0})
	assert.Error(t, err)

	store, _ = newSearchMockStore(t)
	_, err = store.EnsureActiveSearchContract(context.Background(), bootstrapInput(2))
	assert.Error(t, err)

	store, mock := newSearchMockStore(t)
	mock.ExpectExec("SELECT set_config").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM teams").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(searchIDsTeam()))
	called := false
	err = store.withActiveSystemTeamTx(context.Background(), searchIDsTeam(), func(*gorm.DB) error { called = true; return nil })
	require.NoError(t, err)
	assert.True(t, called)
	require.NoError(t, mock.ExpectationsWereMet())

	store, mock = newSearchMockStore(t)
	mock.ExpectExec("SELECT set_config").WillReturnError(errors.New("set team failed"))
	err = store.withActiveSystemTeamTx(context.Background(), searchIDsTeam(), func(*gorm.DB) error { t.Fatal("callback should not run"); return nil })
	assert.Contains(t, err.Error(), "set app.current_team_id")
}

func TestSearchAdapterSelectCanonicalSkipsAndCursorFence(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	current := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{
		TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID,
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "current",
	}, DocumentText: "text", DocumentHash: searchDocumentHash("text")}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationDocumentRows(current, true))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("text", "", int64(0)))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source", "text", "space", "generation"}))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source"}))
	result, err := store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, result)

	store, mock = newSearchMockStore(t)
	retired := current
	retired.SearchState = "not_required"
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationDocumentRows(retired, false))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source", "text", "space", "generation"}))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"team", "owner", "source"}))
	result, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, result)

	runID := "77777777-7777-7777-7777-777777777777"
	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)SELECT selection_cursor_team_id::text").WillReturnRows(sqlmock.NewRows([]string{"team", "kind", "source", "document"}))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationDocumentRows(current, false))
	mock.ExpectExec("(?s)UPDATE search_reconciliation_runs").WillReturnResult(sqlmock.NewResult(0, 0))
	_, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{RunID: runID, EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	assert.Contains(t, err.Error(), "select reconciliation documents")
}

func TestSearchAdapterCompleteStaleAndProviderFenceErrors(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	input := searchmaintenance.SearchDocumentEmbedding{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentHash: "hash", StoredDocumentHash: "hash", EmbeddingDimensions: 2, Embedding: []float32{1, 2}}
	stored := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 2, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "pending"}, DocumentHash: "new"}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(stored))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	stored.SourceVersion = 1
	stored.DocumentHash = "hash"
	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(stored))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnError(errors.New("update failed"))
	_, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	assert.Contains(t, err.Error(), "complete reconciliation documents")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(stored))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	stored.SearchState = "pending"
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{input}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	retired := input
	retired.Retired = true
	retired.SearchDocumentID = documentID
	retired.SourceKind = "evidence"
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{retired}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)
}

func TestSearchAdapterRelationshipProjectionErrorAndNoGeneration(t *testing.T) {
	teamID, ownerID, sourceID, _, _, _ := searchIDs()
	relationship := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: ownerID, SourceID: sourceID, SourceKind: "relationship"}}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{
		"team", "relationship", "owner", "space", "space_generation", "group", "subject", "predicate", "predicate_version", "object_entity", "object_value", "kind", "cardinality", "status", "polarity", "scope", "valid_from", "valid_to", "alias", "support_count", "source_group_count", "version",
	}).AddRow(teamID, sourceID, ownerID, "", int64(0), "group", "subject", "predicate_key", 1, "object", "", "entity", 1, "active", "+", "", nil, nil, "", 1, 1, 2))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnError(errors.New("names failed"))
	_, known, err := canonicalSearchDocument(context.Background(), store.db, relationship)
	require.Error(t, err)
	assert.True(t, known)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{
		"team", "relationship", "owner", "space", "space_generation", "group", "subject", "predicate", "predicate_version", "object_entity", "object_value", "kind", "cardinality", "status", "polarity", "scope", "valid_from", "valid_to", "alias", "support_count", "source_group_count", "version",
	}).AddRow(teamID, sourceID, ownerID, "", int64(0), "group", "subject", "predicate_key", 1, "object", "", "entity", 1, "active", "+", "", nil, nil, "", 1, 1, 2))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"subject", "object", "type", "value", "unit"}).AddRow("Subject", "Object", "", "", ""))
	mock.ExpectQuery("(?s)WITH activated AS").WillReturnRows(sqlmock.NewRows([]string{"generation"}))
	expected, known, err := canonicalSearchDocument(context.Background(), store.db, relationship)
	require.NoError(t, err)
	require.True(t, known)
	require.NotNil(t, expected)
	assert.Empty(t, expected.ProjectionGenerationID)
}

func TestSearchAdapterRemainingPureBranches(t *testing.T) {
	assert.Equal(t, 10, NormalizeExactVectorSearchInput(searchcontract.ExactVectorSearchInput{}).Limit)
	literal, err := VectorLiteral([]float32{1, 2})
	require.NoError(t, err)
	assert.Equal(t, "[1,2]", literal)
	assert.Equal(t, "binary_quantize(embedding)::bit(2)", searchIndexExpressionToken(&searchcontract.ActiveSearchContract{EmbeddingDimensions: 2, IndexStrategy: string(domain.VectorIndexBinaryHNSW)}))
	assert.Equal(t, "halfvec(2)", searchIndexExpressionToken(&searchcontract.ActiveSearchContract{EmbeddingDimensions: 2, IndexStrategy: string(domain.VectorIndexHalfvecHNSW)}))
	assert.Empty(t, searchIndexExpressionToken(&searchcontract.ActiveSearchContract{EmbeddingDimensions: 2, IndexStrategy: string(domain.VectorIndexVectorHNSW)}))
	spec := deriveSearchGenerationSpec("11111111-1111-1111-1111-111111111111", bootstrapInput(2))
	assert.False(t, searchPhysicalIndexNameMatchesSpec("other", spec))
	assert.Nil(t, selectMissingCanonicalSearchDocuments(context.Background(), nil, &searchcontract.ActiveSearchContract{}, 0, &[]searchmaintenance.SearchDocumentForEmbedding{}))
	items := []searchmaintenance.SearchDocumentForEmbedding{{}}
	assert.Nil(t, selectMissingCanonicalSearchDocuments(context.Background(), nil, &searchcontract.ActiveSearchContract{}, 1, &items))
}

func TestSearchAdapterMissingCanonicalStaleVector(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	text := "text"
	hash := searchDocumentHash(text)
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow(text, "", int64(0)))
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state",
	}).AddRow(teamID, documentID, ownerID, "", int64(0), "evidence", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending"))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	updated, err := completeMissingCanonicalSearchDocument(context.Background(), store.db, &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2}, searchmaintenance.SearchDocumentEmbedding{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID, SourceVersion: 1,
		ProjectionFormat: 1, DocumentVersion: 1, DocumentText: text, DocumentHash: hash, Embedding: []float32{.1, .9}, SpaceGeneration: 0,
	})
	require.ErrorIs(t, err, ErrSearchStaleVersion)
	assert.False(t, updated)
}

func TestSearchAdapterRelationshipLoadErrors(t *testing.T) {
	teamID, _, sourceID, _, _, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err := loadRelationshipRecord(context.Background(), store.db, teamID, sourceID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bad"))
	_, err = loadRelationshipRecord(context.Background(), store.db, teamID, sourceID)
	assert.Error(t, err)
}

func TestSearchAdapterRemainingErrorBranches(t *testing.T) {
	input := bootstrapInput(2)
	spec := deriveSearchGenerationSpec("11111111-1111-1111-1111-111111111111", input)
	base := &searchcontract.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2,
		EmbeddingProvider: "openai", EmbeddingModel: "model", DistanceMetric: string(domain.VectorDistanceCosine), VectorNormalization: "provider",
		DocumentFormatVersion: 1, QueryFormatVersion: 1, IndexStrategy: spec.AnnStrategy, OperatorClass: spec.OperatorClass,
		IndexedExpression: spec.IndexedExpression, PhysicalIndexName: spec.PhysicalIndexName,
	}
	for _, mutate := range []func(*searchcontract.ActiveSearchContract){
		func(c *searchcontract.ActiveSearchContract) { c.EmbeddingModel = "other" },
		func(c *searchcontract.ActiveSearchContract) { c.OperatorClass = "other" },
		func(c *searchcontract.ActiveSearchContract) { c.PhysicalIndexName = "other" },
	} {
		candidate := *base
		mutate(&candidate)
		assert.Error(t, validateActiveContractMatchesConfig(&candidate, input))
	}
	assert.False(t, searchPhysicalIndexNameMatchesSpec("v2_other", searchIndexGenerationDefinition{EmbeddingContractID: "bad", EmbeddingDimensions: 2, AnnStrategy: string(domain.VectorIndexVectorHNSW), PhysicalIndexName: ""}))

	teamID, _, _, _, _, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM pg_class AS index_class").WillReturnError(errors.New("index state failed"))
	_, err := loadSearchPhysicalIndexState(context.Background(), store.db, "search_index")
	assert.Contains(t, err.Error(), "index state failed")

	contract := activeSearchContract()
	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM pg_extension")).WillReturnError(errors.New("extension failed"))
	_, err = store.CheckSearchReadiness(context.Background())
	assert.Contains(t, err.Error(), "extension check")

	store, mock = newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery(`(?s)SELECT count\(\*\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT team_id::text, search_document_id::text").WillReturnError(errors.New("vector rows failed"))
	_, err = store.SearchExactVector(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID, QueryEmbedding: []float32{1, 2}})
	assert.Contains(t, err.Error(), "exact vector search")

	for _, invalid := range []searchmaintenance.FinishSearchReconciliationRunInput{
		{RunID: "77777777-7777-7777-7777-777777777777", Status: "unknown"},
		{RunID: "77777777-7777-7777-7777-777777777777", Status: "completed", SelectedCount: -1},
	} {
		assert.Error(t, NewStore(nil, nil).FinishSearchReconciliationRun(context.Background(), invalid))
	}
}

func TestSearchAdapterAdditionalFenceBranches(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	input := searchmaintenance.SearchDocumentEmbedding{TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: sourceID, SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentText: "text", DocumentHash: searchDocumentHash("text"), EmbeddingDimensions: 2, Embedding: []float32{float32(math.NaN()), 1}}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("text", "", int64(0)))
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state"}).AddRow(teamID, documentID, ownerID, "", int64(0), "evidence", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending"))
	updated, err := completeMissingCanonicalSearchDocument(context.Background(), store.db, &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2}, input)
	assert.Error(t, err)
	assert.False(t, updated)

	store, mock = newSearchMockStore(t)
	input.Embedding = []float32{.1, .9}
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("text", "", int64(0)))
	mock.ExpectQuery("(?s)WITH upserted AS").WillReturnRows(sqlmock.NewRows([]string{"team", "document", "owner", "space", "generation", "kind", "source", "version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state"}).AddRow(teamID, documentID, ownerID, "", int64(0), "evidence", sourceID, int64(1), 1, "", int64(1), contractID, 2, "current"))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	updated, err = completeMissingCanonicalSearchDocument(context.Background(), store.db, &searchcontract.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2}, input)
	require.NoError(t, err)
	assert.False(t, updated)

	store, mock = newSearchMockStore(t)
	retired := input
	retired.Retired = true
	retired.SearchDocumentID = documentID
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}).AddRow("text", "", int64(0)))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{retired}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	for _, invalid := range []searchmaintenance.SearchDocumentEmbedding{
		{TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 3, Embedding: []float32{1, 2}, DocumentHash: "hash"},
		{TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1}, DocumentHash: "hash"},
	} {
		_, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{invalid}})
		assert.Error(t, err)
	}
}

func TestSearchAdapterRemainingInputBounds(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	for _, input := range []searchmaintenance.SearchReconciliationSelectionInput{
		{EmbeddingContractID: contractID, EmbeddingDimensions: 0},
		{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 257},
	} {
		_, err := NewStore(nil, nil).SelectSearchReconciliationDocuments(context.Background(), input)
		assert.Error(t, err)
	}
	for _, input := range []searchmaintenance.ApplySearchReconciliationInput{
		{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: make([]searchmaintenance.SearchDocumentEmbedding, 257)},
		{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{{Retired: true, SearchDocumentID: " "}}},
	} {
		_, err := NewStore(nil, nil).CompleteSearchReconciliationDocuments(context.Background(), input)
		assert.Error(t, err)
	}

	teamID, ownerID, sourceID, documentID, _, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)SELECT selection_cursor_team_id::text").WillReturnRows(sqlmock.NewRows([]string{"team", "kind", "source", "document"}).AddRow("bad", "kind", sourceID, documentID))
	_, err := store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{RunID: documentID, EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	assert.Contains(t, err.Error(), "select reconciliation documents")

	// The owner and source identifiers are checked before any SQL is issued.
	for _, document := range []searchmaintenance.SearchDocumentEmbedding{
		{TeamID: "bad", OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: "hash"},
		{TeamID: teamID, OwnerProfileID: "bad", SourceKind: "other", SourceID: sourceID, SourceVersion: 1, DocumentVersion: 1, EmbeddingDimensions: 2, Embedding: []float32{1, 2}, DocumentHash: "hash"},
	} {
		_, err := NewStore(nil, nil).CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{document}})
		assert.Error(t, err)
	}
}

func TestSearchAdapterRelationshipGenerationError(t *testing.T) {
	teamID, ownerID, sourceID, _, _, _ := searchIDs()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM relationship_records").WillReturnRows(sqlmock.NewRows([]string{
		"team", "relationship", "owner", "space", "space_generation", "group", "subject", "predicate", "predicate_version", "object_entity", "object_value", "kind", "cardinality", "status", "polarity", "scope", "valid_from", "valid_to", "alias", "support_count", "source_group_count", "version",
	}).AddRow(teamID, sourceID, ownerID, "", int64(0), "group", "subject", "predicate_key", 1, "object", "", "entity", 1, "active", "+", "", nil, nil, "", 1, 1, 2))
	mock.ExpectQuery("(?s)FROM relationship_records AS relationship").WillReturnRows(sqlmock.NewRows([]string{"subject", "object", "type", "value", "unit"}).AddRow("Subject", "Object", "", "", ""))
	mock.ExpectQuery("(?s)WITH activated AS").WillReturnError(errors.New("generation failed"))
	_, _, err := canonicalSearchDocument(context.Background(), store.db, searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, OwnerProfileID: ownerID, SourceID: sourceID, SourceKind: "relationship"}})
	assert.Contains(t, err.Error(), "generation failed")
}

func TestSearchAdapterCheckConvergenceAttention(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	expectActiveContract(mock, contract)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)SELECT count\(DISTINCT document.team_id\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"class", "count"}).AddRow("state_not_current", 1))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(EXTRACT`).WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(1.0))
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "kind", "source", "source_version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state", "space", "space_generation", "text", "hash", "vector_current", "updated_at",
	}).AddRow(teamID, documentID, ownerID, "other", sourceID, int64(1), 1, "", int64(1), contractID, 2, "pending", "", int64(0), "text", "hash", false, time.Now().UTC()))
	mock.ExpectQuery("(?s)WITH canonical_sources AS").WillReturnRows(sqlmock.NewRows([]string{"team", "count", "seconds"}))
	mock.ExpectQuery("(?s)FROM search_reconciliation_runs").WillReturnRows(sqlmock.NewRows([]string{"run_id", "local_date", "status", "selected", "embedded", "updated", "drifted", "error", "started", "completed", "updated_at"}))
	err := store.CheckSearchConvergence(context.Background())
	require.ErrorIs(t, err, ErrSearchConvergenceAttentionRequired)
}

func TestSearchAdapterCompleteRemainingFenceBranches(t *testing.T) {
	teamID, ownerID, sourceID, documentID, contractID, _ := searchIDs()
	baseInput := searchmaintenance.SearchDocumentEmbedding{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, DocumentHash: "hash", EmbeddingDimensions: 2, Embedding: []float32{.1, .9}}
	baseStored := searchmaintenance.SearchDocumentForEmbedding{SearchDocumentResult: searchmaintenance.SearchDocumentResult{TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID, SourceKind: "other", SourceID: sourceID, SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SearchState: "pending"}, DocumentHash: "hash"}

	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(baseStored))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnResult(sqlmock.NewResult(0, 0))
	result, err := store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{baseInput}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnError(errors.New("document lock failed"))
	_, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{baseInput}})
	assert.Contains(t, err.Error(), "complete reconciliation documents")

	store, mock = newSearchMockStore(t)
	evidenceStored := baseStored
	evidenceStored.SourceKind = "evidence"
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(evidenceStored))
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{baseInput}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(baseStored))
	badVector := baseInput
	badVector.Embedding = []float32{float32(math.NaN()), .9}
	_, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{badVector}})
	assert.Contains(t, err.Error(), "complete reconciliation documents")

	store, mock = newSearchMockStore(t)
	mismatch := baseStored
	mismatch.SourceVersion = 2
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(reconciliationStoredDocumentRows(mismatch))
	result, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{baseInput}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.SkippedCount)

	store, mock = newSearchMockStore(t)
	retired := baseInput
	retired.Retired = true
	retired.SourceKind = "evidence"
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnRows(sqlmock.NewRows([]string{"content", "space_id", "space_generation"}))
	mock.ExpectExec("(?s)UPDATE search_documents").WillReturnError(errors.New("retire failed"))
	_, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{retired}})
	assert.Contains(t, err.Error(), "complete reconciliation documents")

	store, mock = newSearchMockStore(t)
	missing := baseInput
	missing.SearchDocumentID = ""
	missing.SourceKind = "evidence"
	mock.ExpectQuery("(?s)FROM evidence_fragments AS fragment").WillReturnError(errors.New("canonical missing lookup failed"))
	_, err = store.CompleteSearchReconciliationDocuments(context.Background(), searchmaintenance.ApplySearchReconciliationInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Documents: []searchmaintenance.SearchDocumentEmbedding{missing}})
	assert.Contains(t, err.Error(), "complete reconciliation documents")
}

func TestSearchAdapterProjectionAndScanErrors(t *testing.T) {
	contract := activeSearchContract()
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)WITH activated_generation").WillReturnError(errors.New("projection check failed"))
	_, err := store.relationshipProjectionTextIncomplete(context.Background(), contract)
	assert.Contains(t, err.Error(), "relationship projection readiness")

	teamID, _, sourceID, documentID, contractID, _ := searchIDs()
	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "kind", "source", "source_version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state", "space", "space_generation", "text", "hash", "vector_current",
	}).AddRow(teamID, documentID, teamID, "other", sourceID, "bad", 1, "", 1, contractID, 2, "pending", "", 0, "text", "hash", false))
	_, err = store.SelectSearchReconciliationDocuments(context.Background(), searchmaintenance.SearchReconciliationSelectionInput{EmbeddingContractID: contractID, EmbeddingDimensions: 2, Limit: 1})
	assert.Contains(t, err.Error(), "select reconciliation documents")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_index_generations AS generation").WillReturnError(errors.New("active contract failed"))
	_, err = store.contractForVectorSearch(context.Background(), searchcontract.ExactVectorSearchInput{TeamID: teamID})
	assert.Contains(t, err.Error(), "active contract failed")

	store, mock = newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM search_documents AS document").WillReturnRows(sqlmock.NewRows([]string{
		"team", "document", "owner", "kind", "source", "source_version", "projection", "projection_generation", "document_version", "contract", "dimensions", "state", "space", "space_generation", "text", "hash", "vector_current", "updated_at",
	}).AddRow(teamID, documentID, teamID, "other", sourceID, int64(1), 1, "", int64(1), contractID, 2, "current", "", int64(0), "text", "hash", true, "bad"))
	_, err = store.canonicalSearchConvergence(context.Background(), contract)
	assert.Contains(t, err.Error(), "canonical convergence projection")
}

func TestSearchAdapterEnsureBuildsAndActivatesGeneration(t *testing.T) {
	input := bootstrapInput(2)
	contractID := "11111111-1111-1111-1111-111111111111"
	generationID := "77777777-7777-7777-7777-777777777777"
	spec := deriveSearchGenerationSpec(contractID, input)
	contract := &searchcontract.ActiveSearchContract{
		EmbeddingContractID: contractID, SearchIndexGenerationID: generationID, EmbeddingDimensions: 2,
		EmbeddingProvider: "openai", EmbeddingModel: "model", DistanceMetric: string(domain.VectorDistanceCosine), VectorNormalization: "provider",
		DocumentFormatVersion: 1, QueryFormatVersion: 1, IndexGeneration: 1, IndexStrategy: spec.AnnStrategy,
		OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression, PhysicalIndexName: spec.PhysicalIndexName,
		QueryEFSearch: spec.QueryEFSearch, ExactMaxRows: input.ExactMaxRows, CandidateLimit: input.CandidateLimit, AllowExactFallback: spec.AllowExactFallback,
	}
	store, mock := newSearchMockStore(t)
	mock.ExpectQuery("(?s)FROM embedding_contracts").WillReturnRows(embeddingContractRows(contractID, input, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(searchGenerationRows(SearchIndexGenerationDefinition{
		SearchIndexGenerationID: generationID, Generation: 1, EmbeddingContractID: contractID, EmbeddingDimensions: 2,
		AnnStrategy: spec.AnnStrategy, OperatorClass: spec.OperatorClass, IndexedExpression: spec.IndexedExpression,
		PhysicalIndexName: spec.PhysicalIndexName, HNSWM: 16, HNSWEFConstruction: 64, QueryEFSearch: spec.QueryEFSearch,
		ExactMaxRows: input.ExactMaxRows, CandidateLimit: input.CandidateLimit, AllowExactFallback: spec.AllowExactFallback, ActivationState: "building",
	}, 1))
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT index_meta.indisvalid").WillReturnRows(sqlmock.NewRows([]string{"valid"}))
	mock.ExpectExec("(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)FROM search_index_generations").WillReturnRows(sqlmock.NewRows([]string{"contract", "state"}).AddRow(contractID, "building"))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)SET activation_state = 'deprecated'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)SET activation_state = 'active'").WillReturnResult(sqlmock.NewResult(0, 1))
	expectActiveContract(mock, contract)
	mock.ExpectQuery(regexp.QuoteMeta("FROM pg_class AS index_class")).WillReturnRows(sqlmock.NewRows([]string{"valid", "definition"}).AddRow(true,
		"using hnsw (embedding::vector(2) vector_cosine_ops) embedding_contract_id = '"+contractID+"' embedding_dimensions = 2 search_state = 'current' embedding is not null"))
	result, err := store.EnsureActiveSearchContract(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.CreatedPhysicalIndex)
	assert.Equal(t, contractID, result.Contract.EmbeddingContractID)
	require.NoError(t, mock.ExpectationsWereMet())
}
