package embedding

import "context"

// EmbeddingProviderInterface defines the contract for embedding providers.
// Implementations must be safe for concurrent use.
type EmbeddingProviderInterface interface {
	// Embed returns the embedding for a single text along with the model name
	// that produced it. The model name is returned per-call so callers can
	// store it alongside the fragment (AC-41, AC-47).
	Embed(ctx context.Context, text string) ([]float32, string, error)

	// EmbedBatch returns embeddings in the same order as inputs.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error)

	// ModelName returns the configured model identifier.
	ModelName() string

	// Dimensions returns the configured vector length.
	Dimensions() int

	// IsAvailable returns true when the provider is configured to serve requests.
	IsAvailable() bool
}
