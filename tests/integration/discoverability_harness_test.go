package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverabilityHarness_Compiles(t *testing.T) {
	h := NewDiscoverabilityHarness(t)
	t.Cleanup(h.Reset)
	require.NotEmpty(t, h.MockEmbeddingURL())
	require.NotEmpty(t, h.MockEmbeddingToken())
}
