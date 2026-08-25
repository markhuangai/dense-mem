package serverapp

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestNormalizeRememberProcessErrorMapsBoundedIntakeFailures(t *testing.T) {
	conflict := normalizeRememberProcessError(repository.ErrIdempotencyConflict)
	require.ErrorIs(t, conflict, rememberapp.ErrRememberConflict)

	stale := normalizeRememberProcessError(repository.ErrSourceRevisionConflict)
	require.ErrorIs(t, stale, rememberapp.ErrRememberStaleInput)

	preflight := normalizeRememberProcessError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Code: "stale"}}})
	require.ErrorIs(t, preflight, rememberapp.ErrRememberStaleInput)

	budget := normalizeRememberProcessError(&repository.RememberPreflightError{Issues: []repository.RememberPreflightIssue{{Code: "unavailable"}}})
	require.ErrorIs(t, budget, rememberapp.ErrRememberInputBudgetExceeded)

	original := errors.New("database unavailable")
	require.ErrorIs(t, normalizeRememberProcessError(original), original)
	require.Nil(t, normalizeRememberProcessError(nil))
}

func TestRememberAssessmentSnapshotAllocatesRequestOwnedIDsAndExactEvidence(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	snapshot, run := rememberAssessmentSnapshot(rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: uuid.NewString(), SpaceGeneration: 4,
		Evidence: []rememberapp.EvidenceInput{{Content: "first", ContentHash: "hash-1", SourceKey: "source-1", SourceRevisionToken: "rev-1"}, {Content: "second"}},
	}, uuid.NewString(), uuid.NewString())

	require.Len(t, snapshot.Evidence, 2)
	require.Len(t, snapshot.Items, 2)
	require.Equal(t, teamID, run.TeamID)
	require.Equal(t, ownerID, run.OwnerProfileID)
	require.Equal(t, int64(4), run.SpaceGeneration)
	require.NotEmpty(t, snapshot.Evidence[0].FragmentID)
	require.Equal(t, snapshot.Evidence[0].FragmentID, snapshot.Items[0].FragmentID)
	require.Equal(t, "first", snapshot.Evidence[0].Content)
	require.Equal(t, "hash-1", snapshot.Evidence[0].ContentHash)
	require.Equal(t, "source-1", snapshot.Evidence[0].SourceID)
	require.Equal(t, "rev-1", snapshot.Evidence[0].SourceRevisionID)
	require.NotEqual(t, snapshot.Items[0].PlacementItemID, snapshot.Items[1].PlacementItemID)
}

func TestRememberProcessorHelpersBoundValues(t *testing.T) {
	require.Equal(t, "nested", stringValueFromMetadata(map[string]any{"actor": map[string]any{"correlation_id": " nested "}}, "correlation_id"))
	require.Equal(t, "direct", stringValueFromMetadata(map[string]any{"correlation_id": " direct "}, "correlation_id"))
	require.Empty(t, stringValueFromMetadata(nil, "missing"))
	require.Equal(t, "first", firstNonEmptyString(" ", "first", "second"))
	require.Empty(t, firstNonEmptyString(" ", "\t"))
	require.Equal(t, 2, lenMapSlice(map[string]any{"values": []any{"a", "b"}}, "values"))
	require.Equal(t, 1, lenMapSlice(map[string]any{"values": []map[string]any{{"a": 1}}}, "values"))
	require.Zero(t, lenMapSlice(nil, "values"))
}

func TestRememberFailureClassificationPreservesPhaseAndCode(t *testing.T) {
	phase, code, message := classifyRememberFailure(rememberapp.ErrRememberEmbeddingUnavailable, nil)
	require.Equal(t, "embedding", phase)
	require.Equal(t, "embedding_unavailable", code)
	require.NotEmpty(t, message)

	status := &rememberapp.SubmissionStatusResult{
		ProcessingState: "failed",
		Errors:          []rememberapp.SubmissionStatusError{rememberapp.StatusError(rememberapp.SubmissionErrorCommitConflict)},
	}
	phase, code, _ = classifyRememberFailure(rememberapp.ErrRememberPersistence, status)
	require.Equal(t, "semantic_commit", phase)
	require.Equal(t, "commit_conflict", code)

	failed := rememberFailureStatus("submission-id", code, "safe message")
	require.Equal(t, "submission-id", failed.SubmissionID)
	require.Equal(t, "failed", failed.ProcessingState)
	require.Equal(t, "commit_conflict", failed.Errors[0].Code)
}

func TestRememberProcessorEmbedsDeduplicatedDocumentsAndMapsProviderBoundaries(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	documentID := uuid.NewString()
	inline := &rememberProcessorInlineStub{documents: []repository.SearchDocumentForEmbedding{{
		SearchDocumentResult: repository.SearchDocumentResult{
			TeamID: teamID, SearchDocumentID: documentID, OwnerProfileID: ownerID,
			SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1,
			EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 3,
			SpaceID: uuid.NewString(), SpaceGeneration: 1,
		}, DocumentText: "document text",
	}}}
	embedder := &rememberProcessorEmbedderStub{model: "embed-model", vectors: [][]float32{{1, 2, 3}}}
	processor := &rememberSynchronousProcessor{inline: inline, embedder: embedder}

	err := processor.embedSearchDocuments(context.Background(), teamID, ownerID, &repository.CreateIngestResult{Items: []repository.PlacementItem{
		{Result: map[string]any{"search_document_ids": []any{documentID, documentID}}},
	}})
	require.NoError(t, err)
	require.Len(t, inline.completed.Documents, 1)
	require.Equal(t, documentID, inline.completed.Documents[0].SearchDocumentID)
	require.Equal(t, []string{"document text"}, embedder.texts)

	inline.loadErr = errors.New("load failed")
	require.Error(t, processor.embedSearchDocuments(context.Background(), teamID, ownerID, &repository.CreateIngestResult{Items: []repository.PlacementItem{{Result: map[string]any{"search_document_ids": []string{documentID}}}}}))

	inline.loadErr = nil
	processor.embedder = &rememberProcessorEmbedderStub{model: "embed-model", unavailable: true}
	require.ErrorIs(t, processor.embedSearchDocuments(context.Background(), teamID, ownerID, &repository.CreateIngestResult{Items: []repository.PlacementItem{{Result: map[string]any{"search_document_ids": []string{documentID}}}}}), rememberapp.ErrRememberEmbeddingUnavailable)
}

func TestRememberProcessorEmbeddingValidationAndBounds(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	makePlacement := func(ids []string) *repository.CreateIngestResult {
		values := make([]any, len(ids))
		for index, id := range ids {
			values[index] = id
		}
		return &repository.CreateIngestResult{Items: []repository.PlacementItem{{Result: map[string]any{"search_document_ids": values}}}}
	}
	processor := &rememberSynchronousProcessor{
		inline:   &rememberProcessorInlineStub{documents: []repository.SearchDocumentForEmbedding{{SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: uuid.NewString(), SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 2, SpaceGeneration: 1}, DocumentText: "text"}}},
		embedder: &rememberProcessorEmbedderStub{model: "configured", vectors: [][]float32{{1, 2}}},
	}
	tooMany := make([]string, 257)
	for index := range tooMany {
		tooMany[index] = uuid.NewString()
	}
	require.ErrorIs(t, processor.embedSearchDocuments(context.Background(), teamID, ownerID, makePlacement(tooMany)), rememberapp.ErrRememberInputBudgetExceeded)

	processor.inline.(*rememberProcessorInlineStub).documents[0].EmbeddingDimensions = 2
	processor.embedder.(*rememberProcessorEmbedderStub).vectors = [][]float32{{1}}
	require.ErrorIs(t, processor.embedSearchDocuments(context.Background(), teamID, ownerID, makePlacement([]string{processor.inline.(*rememberProcessorInlineStub).documents[0].SearchDocumentID})), rememberapp.ErrRememberEmbeddingInvalid)

	processor.embedder.(*rememberProcessorEmbedderStub).vectors = [][]float32{{float32(math.NaN()), 2}}
	// The provider output is rejected before any completion write.
	require.ErrorIs(t, processor.embedSearchDocuments(context.Background(), teamID, ownerID, makePlacement([]string{processor.inline.(*rememberProcessorInlineStub).documents[0].SearchDocumentID})), rememberapp.ErrRememberEmbeddingInvalid)
}

var _ embedding.EmbeddingProviderInterface = (*rememberProcessorEmbedderStub)(nil)

type rememberProcessorInlineStub struct {
	documents   []repository.SearchDocumentForEmbedding
	loadErr     error
	completed   repository.CompleteSearchDocumentsWithEmbeddingsInput
	completeErr error
}

func (s *rememberProcessorInlineStub) LoadSearchDocumentsForEmbedding(context.Context, repository.LoadSearchDocumentsForEmbeddingInput) ([]repository.SearchDocumentForEmbedding, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.documents, nil
}

func (*rememberProcessorInlineStub) LoadSearchDocumentsForSources(context.Context, repository.LoadSearchDocumentsForSourcesInput) ([]repository.SearchDocumentForEmbedding, error) {
	return nil, nil
}

func (s *rememberProcessorInlineStub) CompleteSearchDocumentsWithEmbeddings(_ context.Context, input repository.CompleteSearchDocumentsWithEmbeddingsInput) error {
	s.completed = input
	return s.completeErr
}

type rememberProcessorEmbedderStub struct {
	model       string
	vectors     [][]float32
	unavailable bool
	texts       []string
	err         error
}

func (s *rememberProcessorEmbedderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, s.model, s.err
}

func (s *rememberProcessorEmbedderStub) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	s.texts = append([]string(nil), texts...)
	return s.vectors, s.model, s.err
}

func (s *rememberProcessorEmbedderStub) ModelName() string { return s.model }
func (s *rememberProcessorEmbedderStub) Dimensions() int {
	if len(s.vectors) == 0 {
		return 0
	}
	return len(s.vectors[0])
}
func (s *rememberProcessorEmbedderStub) IsAvailable() bool { return !s.unavailable && s.model != "" }
