package serverapp

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberAssessmentSnapshotAllocatesRequestOwnedIDs(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	snapshot, run := rememberAssessmentSnapshot(rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: uuid.NewString(), SpaceGeneration: 4,
		Evidence: []rememberapp.EvidenceInput{{Content: "first", ContentHash: "hash-1", SourceKey: "source-1", SourceRevisionToken: "rev-1"}, {Content: "second"}},
	}, uuid.NewString())

	require.Len(t, snapshot.Evidence, 2)
	require.Len(t, snapshot.Items, 2)
	require.Equal(t, teamID, run.TeamID)
	require.Equal(t, ownerID, run.OwnerProfileID)
	require.Equal(t, int64(4), run.SpaceGeneration)
	require.NotEmpty(t, snapshot.Evidence[0].FragmentID)
	require.Equal(t, snapshot.Evidence[0].FragmentID, snapshot.Items[0].Fragment.FragmentID)
	require.NotEqual(t, snapshot.Items[0].ItemID, snapshot.Items[1].ItemID)
}

func TestRememberProcessorEmbedsOneValidatedBatch(t *testing.T) {
	processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{
		model: "embed-model", vectors: [][]float32{{1, 2, 3}, {4, 5, 6}},
	}}
	documents := []repository.SearchDocumentForEmbedding{
		{SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document-a", EmbeddingDimensions: 3}, DocumentText: "first"},
		{SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document-b", EmbeddingDimensions: 3}, DocumentText: "second"},
	}

	result, err := processor.embedSearchDocumentBatch(context.Background(), documents)
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, []float32{1, 2, 3}, result[0].Embedding)
	require.Equal(t, []string{"first", "second"}, processor.embedder.(*rememberProcessorEmbedderStub).texts)
}

func TestRememberProcessorRejectsInvalidEmbeddingResponsesBeforeCommit(t *testing.T) {
	for _, test := range []struct {
		name    string
		vectors [][]float32
	}{
		{name: "wrong dimensions", vectors: [][]float32{{1}}},
		{name: "non finite", vectors: [][]float32{{float32(math.NaN()), 2}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{model: "embed-model", vectors: test.vectors}}
			_, err := processor.embedSearchDocumentBatch(context.Background(), []repository.SearchDocumentForEmbedding{{
				SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document", EmbeddingDimensions: 2}, DocumentText: "text",
			}})
			require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
		})
	}
}

var _ embedding.EmbeddingProviderInterface = (*rememberProcessorEmbedderStub)(nil)

type rememberProcessorEmbedderStub struct {
	model       string
	vectors     [][]float32
	texts       []string
	unavailable bool
}

func (s *rememberProcessorEmbedderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, s.model, nil
}

func (s *rememberProcessorEmbedderStub) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	s.texts = append([]string(nil), texts...)
	return s.vectors, s.model, nil
}

func (s *rememberProcessorEmbedderStub) ModelName() string { return s.model }
func (s *rememberProcessorEmbedderStub) Dimensions() int {
	if len(s.vectors) == 0 {
		return 0
	}
	return len(s.vectors[0])
}
func (s *rememberProcessorEmbedderStub) IsAvailable() bool { return !s.unavailable && s.model != "" }
