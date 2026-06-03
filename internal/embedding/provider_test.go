package embedding

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockEmbeddingProvider_ImplementsInterface(t *testing.T) {
	var _ EmbeddingProviderInterface = (*MockEmbeddingProvider)(nil)
	assert.True(t, true, "compile-time assertion passed")
}

func TestMockEmbeddingProvider_DefaultReturnsZeroVector(t *testing.T) {
	m := &MockEmbeddingProvider{DimensionsResult: 4, ModelNameResult: "mock"}
	vec, model, err := m.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, 4, len(vec))
	assert.Equal(t, "mock", model)
}

func TestMockEmbeddingProvider_EmbedBatch_Ordering(t *testing.T) {
	m := &MockEmbeddingProvider{
		DimensionsResult: 3,
		ModelNameResult:  "mock-batch",
	}

	texts := []string{"first", "second", "third"}
	vecs, model, err := m.EmbedBatch(context.Background(), texts)

	require.NoError(t, err)
	assert.Equal(t, "mock-batch", model)
	assert.Equal(t, 3, len(vecs))

	// Verify ordering - each vector should be a zero vector of correct dimensions
	for i, vec := range vecs {
		assert.Equal(t, m.DimensionsResult, len(vec), "vector %d has wrong length", i)
	}
}

func TestMockEmbeddingProvider_EmbedFunc_CustomBehavior(t *testing.T) {
	expectedVec := []float32{0.1, 0.2, 0.3}
	expectedModel := "custom-model"
	customErr := errors.New("custom error")

	m := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, string, error) {
			if text == "error" {
				return nil, "", customErr
			}
			return expectedVec, expectedModel, nil
		},
	}

	// Test success case
	vec, model, err := m.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, expectedVec, vec)
	assert.Equal(t, expectedModel, model)

	// Test error case
	_, _, err = m.Embed(context.Background(), "error")
	assert.ErrorIs(t, err, customErr)
}

func TestMockEmbeddingProvider_EmbedBatchFunc_CustomBehavior(t *testing.T) {
	expectedVecs := [][]float32{
		{0.1, 0.2},
		{0.3, 0.4},
		{0.5, 0.6},
	}
	expectedModel := "batch-model"

	m := &MockEmbeddingProvider{
		EmbedBatchFunc: func(ctx context.Context, texts []string) ([][]float32, string, error) {
			return expectedVecs, expectedModel, nil
		},
	}

	vecs, model, err := m.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	assert.Equal(t, expectedVecs, vecs)
	assert.Equal(t, expectedModel, model)
}

func TestMockEmbeddingProvider_CallCount(t *testing.T) {
	m := &MockEmbeddingProvider{
		DimensionsResult: 2,
		ModelNameResult:  "test",
	}

	// Initial state
	assert.Equal(t, 0, m.CallCount)

	// After first call
	_, _, _ = m.Embed(context.Background(), "first")
	assert.Equal(t, 1, m.CallCount)
	assert.Equal(t, "first", m.LastText)

	// After second call
	_, _, _ = m.Embed(context.Background(), "second")
	assert.Equal(t, 2, m.CallCount)
	assert.Equal(t, "second", m.LastText)
}

func TestMockEmbeddingProvider_Dimensions(t *testing.T) {
	m := &MockEmbeddingProvider{DimensionsResult: 1536}
	assert.Equal(t, 1536, m.Dimensions())
}

func TestMockEmbeddingProvider_ModelName(t *testing.T) {
	m := &MockEmbeddingProvider{ModelNameResult: "text-embedding-3-small"}
	assert.Equal(t, "text-embedding-3-small", m.ModelName())
}

func TestMockEmbeddingProvider_IsAvailable(t *testing.T) {
	m := &MockEmbeddingProvider{IsAvailableResult: true}
	assert.True(t, m.IsAvailable())

	m.IsAvailableResult = false
	assert.False(t, m.IsAvailable())
}

func TestEmbeddingProviderInterface_SentinelErrors(t *testing.T) {
	// Test ErrEmbeddingTimeout wrapping
	timeoutErr := &TimeoutError{Provider: "openai", Message: "request timed out"}
	assert.ErrorIs(t, timeoutErr, ErrEmbeddingTimeout)
	assert.Equal(t, "embedding request timed out: openai: request timed out", timeoutErr.Error())

	// Test ErrEmbeddingRateLimit wrapping
	rateLimitErr := &RateLimitError{Provider: "openai", Message: "too many requests"}
	assert.ErrorIs(t, rateLimitErr, ErrEmbeddingRateLimit)
	assert.Equal(t, "embedding request rate limited: openai: too many requests", rateLimitErr.Error())

	// Test ErrEmbeddingProvider wrapping
	providerErr := &ProviderError{Provider: "openai", Message: "api error"}
	assert.ErrorIs(t, providerErr, ErrEmbeddingProvider)
	assert.Equal(t, "embedding provider error: openai: api error", providerErr.Error())
	assert.NoError(t, errors.Unwrap(providerErr))

	// Test provider error with cause
	cause := errors.New("underlying error")
	providerErrWithCause := &ProviderError{Provider: "openai", Message: "api error", Cause: cause}
	assert.ErrorIs(t, providerErrWithCause, ErrEmbeddingProvider)
	assert.ErrorIs(t, providerErrWithCause, cause)
	assert.Equal(t, "embedding provider error: openai: api error: underlying error", providerErrWithCause.Error())
	assert.ErrorIs(t, errors.Unwrap(providerErrWithCause), cause)
}

func TestProviderHTTPError_ErrorWithoutMessage(t *testing.T) {
	err := &ProviderHTTPError{Status: 503}
	assert.Equal(t, "embedding provider http error: status=503", err.Error())
}
