package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSynchronousRememberEmbeddingResultRejectsDuplicateHashes(t *testing.T) {
	result := SynchronousRememberEmbeddingResult{
		EmbeddingContractID:     uuid.NewString(),
		EmbeddingDimensions:     2,
		EmbeddingModel:          "embedding-test",
		SearchGenerationID:      uuid.NewString(),
		SearchGenerationVersion: 1,
		Embeddings: []SynchronousRememberEmbedding{
			{DocumentHash: searchDocumentTextHash("same"), Vector: []float32{1, 0}},
			{DocumentHash: searchDocumentTextHash("same"), Vector: []float32{1, 0}},
		},
	}

	err := validateSynchronousRememberEmbeddingResult(result)

	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingFence)
}

func TestSearchDocumentTextHashNormalizesWhitespace(t *testing.T) {
	require.Equal(t, searchDocumentTextHash("document"), searchDocumentTextHash(" document "))
}
