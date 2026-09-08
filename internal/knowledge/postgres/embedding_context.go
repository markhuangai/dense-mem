package postgres

import "context"

type inlineEmbeddingResultsContextKey struct{}

func WithInlineEmbeddingResults(ctx context.Context, results []InlineEmbeddingResult) context.Context {
	copyResults := make([]InlineEmbeddingResult, len(results))
	copy(copyResults, results)
	return context.WithValue(ctx, inlineEmbeddingResultsContextKey{}, copyResults)
}

func inlineEmbeddingResults(ctx context.Context) []InlineEmbeddingResult {
	results, _ := ctx.Value(inlineEmbeddingResultsContextKey{}).([]InlineEmbeddingResult)
	return results
}
