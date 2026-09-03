package serverapp

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func inlineEmbeddingResultsFromDocuments(
	documents []repository.SearchDocumentEmbedding,
	plan *repository.InlineEmbeddingPlan,
) []repository.InlineEmbeddingResult {
	if plan == nil || len(documents) == 0 {
		return []repository.InlineEmbeddingResult{}
	}
	results := make([]repository.InlineEmbeddingResult, 0, len(documents))
	for _, document := range documents {
		results = append(results, repository.InlineEmbeddingResult{
			DocumentHash:            document.DocumentHash,
			Embedding:               append([]float32(nil), document.Embedding...),
			EmbeddingContractID:     plan.EmbeddingContractID,
			EmbeddingDimensions:     plan.EmbeddingDimensions,
			EmbeddingModel:          plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration:         plan.IndexGeneration,
		})
	}
	return results
}

func inlineEmbeddingResultsFromDuplicateDocuments(
	documents []repository.SearchDocumentEmbedding,
	plan *repository.RememberDuplicateEmbeddingPlan,
) []repository.InlineEmbeddingResult {
	if plan == nil || len(documents) == 0 {
		return []repository.InlineEmbeddingResult{}
	}
	results := make([]repository.InlineEmbeddingResult, 0, len(documents))
	for _, document := range documents {
		results = append(results, repository.InlineEmbeddingResult{
			DocumentHash:            document.DocumentHash,
			Embedding:               append([]float32(nil), document.Embedding...),
			EmbeddingContractID:     plan.EmbeddingContractID,
			EmbeddingDimensions:     plan.EmbeddingDimensions,
			EmbeddingModel:          plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration:         plan.IndexGeneration,
		})
	}
	return results
}

func mergeInlineEmbeddingResults(groups ...[]repository.InlineEmbeddingResult) []repository.InlineEmbeddingResult {
	merged := make([]repository.InlineEmbeddingResult, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, result := range group {
			hash := strings.TrimSpace(result.DocumentHash)
			if _, exists := seen[hash]; exists {
				continue
			}
			seen[hash] = struct{}{}
			result.Embedding = append([]float32(nil), result.Embedding...)
			merged = append(merged, result)
		}
	}
	return merged
}
