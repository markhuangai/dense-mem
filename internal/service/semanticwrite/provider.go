package semanticwrite

import (
	"context"
	"errors"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
)

// PlainBatchProvider is the existing ordered-vector provider boundary.
type PlainBatchProvider interface {
	EmbedBatch(context.Context, []string) ([][]float32, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}

type semanticwriteEmbeddingAdapter struct {
	provider PlainBatchProvider
}

var _ BatchProvider = semanticwriteEmbeddingAdapter{}

func NewProviderExecutor(provider PlainBatchProvider) *Executor {
	return NewExecutor(semanticwriteEmbeddingAdapter{provider: provider})
}

func (a semanticwriteEmbeddingAdapter) EmbedBatch(ctx context.Context, texts []string) ([]IndexedEmbedding, string, error) {
	if a.provider == nil {
		return nil, "", errors.New("embedding provider is required")
	}
	vectors, model, err := a.provider.EmbedBatch(ctx, texts)
	if err != nil {
		metadata := embeddingcontract.ClassifyFailure(err)
		if metadata.Code == "provider_response_invalid" {
			return nil, "", &ExecutionError{Reason: ReasonProviderCall, Kind: ErrProviderResponseInvalid, Cause: err}
		}
		if metadata.Code == "provider_timeout" {
			return nil, "", &ExecutionError{Reason: ReasonProviderCall, Kind: ErrProviderTimeout, Cause: err}
		}
		return nil, "", err
	}
	result := make([]IndexedEmbedding, len(vectors))
	for index, vector := range vectors {
		result[index] = IndexedEmbedding{Index: index, Vector: append([]float32(nil), vector...)}
	}
	return result, model, nil
}

func (a semanticwriteEmbeddingAdapter) ModelName() string {
	if a.provider == nil {
		return ""
	}
	return a.provider.ModelName()
}

func (a semanticwriteEmbeddingAdapter) Dimensions() int {
	if a.provider == nil {
		return 0
	}
	return a.provider.Dimensions()
}

func (a semanticwriteEmbeddingAdapter) IsAvailable() bool {
	return a.provider != nil && a.provider.IsAvailable()
}
