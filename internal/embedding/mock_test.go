package embedding

import (
	"context"
	"sync"
)

// MockEmbeddingProvider is a mock implementation of EmbeddingProviderInterface
// for use in unit tests. It supports configurable behavior via function fields
// and tracks calls for assertions.
type MockEmbeddingProvider struct {
	mu sync.Mutex

	// EmbedFunc is called by Embed. If nil, returns a zero vector of DimensionsResult length.
	EmbedFunc func(ctx context.Context, text string) ([]float32, string, error)

	// EmbedBatchFunc is called by EmbedBatch. If nil, returns zero vectors for each input.
	EmbedBatchFunc func(ctx context.Context, texts []string) ([][]float32, string, error)

	// DimensionsResult is returned by Dimensions.
	DimensionsResult int

	// ModelNameResult is returned by ModelName.
	ModelNameResult string

	// IsAvailableResult is returned by IsAvailable.
	IsAvailableResult bool

	// CallCount tracks the number of calls to Embed.
	CallCount int

	// LastText tracks the most recent text passed to Embed.
	LastText string
}

// Compile-time assertion that MockEmbeddingProvider implements EmbeddingProviderInterface.
var _ EmbeddingProviderInterface = (*MockEmbeddingProvider)(nil)

// Embed calls EmbedFunc if set, otherwise returns a zero vector of DimensionsResult length.
func (m *MockEmbeddingProvider) Embed(ctx context.Context, text string) ([]float32, string, error) {
	m.mu.Lock()
	m.CallCount++
	m.LastText = text
	m.mu.Unlock()

	if m.EmbedFunc != nil {
		return m.EmbedFunc(ctx, text)
	}

	// Default: return zero vector of configured dimensions
	vec := make([]float32, m.DimensionsResult)
	return vec, m.ModelNameResult, nil
}

// EmbedBatch calls EmbedBatchFunc if set, otherwise returns zero vectors for each input.
func (m *MockEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	if m.EmbedBatchFunc != nil {
		return m.EmbedBatchFunc(ctx, texts)
	}

	// Default: return zero vectors for each input
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = make([]float32, m.DimensionsResult)
	}
	return result, m.ModelNameResult, nil
}

// ModelName returns ModelNameResult.
func (m *MockEmbeddingProvider) ModelName() string {
	return m.ModelNameResult
}

// Dimensions returns DimensionsResult.
func (m *MockEmbeddingProvider) Dimensions() int {
	return m.DimensionsResult
}

// IsAvailable returns IsAvailableResult.
func (m *MockEmbeddingProvider) IsAvailable() bool {
	return m.IsAvailableResult
}
