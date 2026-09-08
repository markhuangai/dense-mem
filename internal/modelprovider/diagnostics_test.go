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
