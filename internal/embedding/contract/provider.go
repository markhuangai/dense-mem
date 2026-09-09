// Package contract owns the provider-independent embedding boundary.
package contract

import "context"

// EmbeddingProviderInterface defines the contract for embedding providers.
// Implementations must be safe for concurrent use.
type EmbeddingProviderInterface interface {
	Embed(ctx context.Context, text string) ([]float32, string, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}
