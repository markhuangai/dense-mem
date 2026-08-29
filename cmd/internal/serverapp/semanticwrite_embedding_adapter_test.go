package serverapp

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/stretchr/testify/require"
)

func TestSemanticwriteEmbeddingAdapterPreservesProviderOrder(t *testing.T) {
	adapter := semanticwriteEmbeddingAdapter{provider: semanticwriteEmbeddingProviderStub{}}
	result, model, err := adapter.EmbedBatch(context.Background(), []string{"first", "second"})
	require.NoError(t, err)
	require.Equal(t, "fixture", model)
	require.Equal(t, 2, len(result))
	require.Equal(t, 0, result[0].Index)
	require.Equal(t, []float32{1, 2}, result[0].Vector)
	require.Equal(t, 1, result[1].Index)
	require.Equal(t, []float32{3, 4}, result[1].Vector)
}

func TestSemanticwriteEmbeddingAdapterClassifiesProviderResponseContractErrors(t *testing.T) {
	adapter := semanticwriteEmbeddingAdapter{provider: semanticwriteEmbeddingProviderStub{
		err: &embedding.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"},
	}}
	_, _, err := adapter.EmbedBatch(context.Background(), []string{"invalid"})
	require.ErrorIs(t, err, semanticwrite.ErrProviderResponseInvalid)
}

func TestSemanticwriteEmbeddingAdapterClassifiesSanitizedRetryProviderErrors(t *testing.T) {
	inner := semanticwriteEmbeddingProviderStub{
		err: &embedding.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"},
	}
	retry := embedding.NewRetryEmbeddingProviderWithKey(inner, semanticwriteEmbeddingTestLogger{}, "")
	adapter := semanticwriteEmbeddingAdapter{provider: retry}

	_, _, err := adapter.EmbedBatch(context.Background(), []string{"invalid"})
	require.ErrorIs(t, err, semanticwrite.ErrProviderResponseInvalid)
}

func TestSemanticwriteEmbeddingExecutorBoundsProviderActionFailuresAsUnavailable(t *testing.T) {
	executor := newSemanticwriteEmbeddingExecutor(semanticwriteEmbeddingProviderStub{
		err: &embedding.ProviderError{FailureCode: "provider_authentication_failed", FailureClass: "provider_action_required"},
	})

	_, err := executor.Execute(context.Background(), semanticwrite.Plan{
		Documents: []semanticwrite.Document{{Hash: "document-hash", Text: "document text"}},
		Fence: semanticwrite.Fence{
			Model: "fixture", Dimensions: 2, EmbeddingContractID: "contract",
			SearchGenerationID: "generation", SearchGenerationVersion: 1,
		},
		Timeout: time.Second,
	})

	require.ErrorIs(t, err, semanticwrite.ErrProviderUnavailable)
	require.NotContains(t, err.Error(), "provider_authentication_failed")
}

type semanticwriteEmbeddingProviderStub struct {
	err error
}

var _ embedding.EmbeddingProviderInterface = semanticwriteEmbeddingProviderStub{}

func (semanticwriteEmbeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return []float32{1, 2}, "fixture", nil
}

func (p semanticwriteEmbeddingProviderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	if p.err != nil {
		return nil, "", p.err
	}
	return [][]float32{{1, 2}, {3, 4}}, "fixture", nil
}

func (semanticwriteEmbeddingProviderStub) ModelName() string { return "fixture" }
func (semanticwriteEmbeddingProviderStub) Dimensions() int   { return 2 }
func (semanticwriteEmbeddingProviderStub) IsAvailable() bool { return true }

type semanticwriteEmbeddingTestLogger struct{}

func (semanticwriteEmbeddingTestLogger) Info(string, ...observability.LogAttr)         {}
func (semanticwriteEmbeddingTestLogger) Error(string, error, ...observability.LogAttr) {}
func (semanticwriteEmbeddingTestLogger) Warn(string, ...observability.LogAttr)         {}
func (semanticwriteEmbeddingTestLogger) Debug(string, ...observability.LogAttr)        {}
func (l semanticwriteEmbeddingTestLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}
