package serverapp

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

type semanticwriteEmbeddingAdapter struct {
	provider embedding.EmbeddingProviderInterface
}

var _ semanticwrite.BatchProvider = semanticwriteEmbeddingAdapter{}

func newSemanticwriteEmbeddingExecutor(provider embedding.EmbeddingProviderInterface) *semanticwrite.Executor {
	return semanticwrite.NewExecutor(semanticwriteEmbeddingAdapter{provider: provider})
}

func (a semanticwriteEmbeddingAdapter) EmbedBatch(ctx context.Context, texts []string) ([]semanticwrite.IndexedEmbedding, string, error) {
	if a.provider == nil {
		return nil, "", errors.New("embedding provider is required")
	}
	vectors, model, err := a.provider.EmbedBatch(ctx, texts)
	if err != nil {
		metadata := embedding.ClassifyFailure(err)
		switch {
		case metadata.Code == "provider_response_invalid":
			return nil, "", semanticwrite.ErrProviderResponseInvalid
		case metadata.Class == "provider_action_required":
			return nil, "", semanticwrite.ErrProviderConfiguration
		}
		return nil, "", err
	}
	result := make([]semanticwrite.IndexedEmbedding, len(vectors))
	for index, vector := range vectors {
		result[index] = semanticwrite.IndexedEmbedding{Index: index, Vector: append([]float32(nil), vector...)}
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
