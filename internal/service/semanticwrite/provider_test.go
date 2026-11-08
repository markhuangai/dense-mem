package semanticwrite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
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
		err: &embeddingcontract.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"},
	}}
	_, _, err := adapter.EmbedBatch(context.Background(), []string{"invalid"})
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
}

func TestSemanticwriteEmbeddingAdapterClassifiesSanitizedRetryProviderErrors(t *testing.T) {
	inner := semanticwriteEmbeddingProviderStub{
		err: &embeddingcontract.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"},
	}
	retry := embedding.NewRetryEmbeddingProviderWithKey(inner, semanticwriteEmbeddingTestLogger{}, "")
	adapter := semanticwriteEmbeddingAdapter{provider: retry}

	_, _, err := adapter.EmbedBatch(context.Background(), []string{"invalid"})
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
}

func TestSemanticwriteEmbeddingExecutorBoundsProviderActionFailuresAsUnavailable(t *testing.T) {
	executor := NewProviderExecutor(semanticwriteEmbeddingProviderStub{
		err: &embeddingcontract.ProviderError{FailureCode: "provider_authentication_failed", FailureClass: "provider_action_required"},
	})

	_, err := executor.Execute(context.Background(), Plan{
		Documents: []Document{{Hash: "document-hash", Text: "document text"}},
		Fence: Fence{
			Model: "fixture", Dimensions: 2, EmbeddingContractID: "contract",
			SearchGenerationID: "generation", SearchGenerationVersion: 1,
		},
		Timeout: time.Second,
	})

	require.ErrorIs(t, err, ErrProviderUnavailable)
	require.NotContains(t, err.Error(), "provider_authentication_failed")
}

func TestSemanticwriteEmbeddingExecutorPreservesHTTPFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		clientTimeout bool
		want          error
	}{
		{name: "success", status: http.StatusOK},
		{name: "provider timeout", status: http.StatusRequestTimeout, want: ErrProviderTimeout},
		{name: "client timeout", clientTimeout: true, want: ErrProviderTimeout},
		{name: "unavailable", status: http.StatusServiceUnavailable, want: ErrProviderUnavailable},
		{name: "rate limited", status: http.StatusTooManyRequests, want: ErrProviderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if test.clientTimeout {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "0.001")
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_, _ = io.WriteString(w, `{"data":[{"embedding":[0.25,0.5]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
					return
				}
				_, _ = io.WriteString(w, `{"error":{"message":"private provider failure"}}`)
			}))
			defer server.Close()
			client := server.Client()
			if test.clientTimeout {
				client.Timeout = 20 * time.Millisecond
			}
			provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{
				AIAPIURL: server.URL, AIAPIKey: "fixture-key", AIEmbeddingModel: "fixture", AIEmbeddingDimensions: 2,
			}, client)
			retry := embedding.NewRetryEmbeddingProviderWithKeyAndOptions(provider, semanticwriteEmbeddingTestLogger{}, "fixture-key", embedding.RetryEmbeddingOptions{
				MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
			})
			executor := NewProviderExecutor(retry)
			result, err := executor.Execute(context.Background(), Plan{
				Documents: []Document{{Hash: "document-hash", Text: "document text"}},
				Fence: Fence{
					Model: "fixture", Dimensions: 2, EmbeddingContractID: "contract",
					SearchGenerationID: "generation", SearchGenerationVersion: 1,
				},
				Timeout: 2 * time.Second,
			})
			if test.want == nil {
				require.NoError(t, err)
				require.Equal(t, []float32{0.25, 0.5}, result.Embeddings[0].Vector)
				return
			}
			require.ErrorIs(t, err, test.want)
			require.Empty(t, result.Embeddings)
			require.NotContains(t, err.Error(), "private provider failure")
			if test.want == ErrProviderTimeout {
				require.NotErrorIs(t, err, ErrProviderUnavailable)
			}
		})
	}
}

type semanticwriteEmbeddingProviderStub struct {
	err error
}

var _ embeddingcontract.EmbeddingProviderInterface = semanticwriteEmbeddingProviderStub{}

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
