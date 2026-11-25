package modelprovider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type diagnosticsTestContextKey string
type diagnosticsTestRecorder struct{}

func (diagnosticsTestRecorder) RecordProviderExchange(context.Context, ProviderExchange) {}

func TestExchangeRecorderContextRoundTrip(t *testing.T) {
	const key diagnosticsTestContextKey = "diagnostic"
	base := context.WithValue(context.Background(), key, "kept")
	recorder := diagnosticsTestRecorder{}
	wrapped := WithExchangeRecorder(base, recorder)

	require.Equal(t, "kept", wrapped.Value(key))
	require.Equal(t, recorder, ExchangeRecorderFromContext(wrapped))
	require.Equal(t, base, WithExchangeRecorder(base, nil))
	require.Nil(t, ExchangeRecorderFromContext(context.Background()))
}

func TestProjectEmbeddingProviderResponseUsesDecodedDimensions(t *testing.T) {
	response := ProjectEmbeddingProviderResponse(
		[]byte(`{"model":"embedding-model","data":[{"index":4,"object":"embedding","embedding":[0.1,0.2]}]}`),
		[]int{2},
	)
	require.Equal(t, `{"data":[{"embedding_dimensions":2,"index":4,"object":"embedding"}],"model":"embedding-model"}`, string(response))
}
