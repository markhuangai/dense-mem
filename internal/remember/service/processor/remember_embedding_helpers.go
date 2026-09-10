package processor

import (
	"fmt"
	"strings"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
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

type rememberEmbeddingPlanFailure struct {
	cause error
}

func (e *rememberEmbeddingPlanFailure) Error() string {
	return "remember embedding plan failed"
}

func (e *rememberEmbeddingPlanFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type rememberEmbeddingConfigurationFailure struct{}

func (*rememberEmbeddingConfigurationFailure) Error() string {
	return rememberapp.ErrRememberEmbeddingUnavailable.Error()
}

func (*rememberEmbeddingConfigurationFailure) Unwrap() error {
	return rememberapp.ErrRememberEmbeddingUnavailable
}

type rememberEmbeddingProviderFailure struct {
	cause error
}

func (e *rememberEmbeddingProviderFailure) Error() string {
	if e == nil || e.cause == nil {
		return rememberapp.ErrRememberEmbeddingUnavailable.Error()
	}
	return fmt.Sprintf("%v: %v", rememberapp.ErrRememberEmbeddingUnavailable, e.cause)
}

func (e *rememberEmbeddingProviderFailure) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{rememberapp.ErrRememberEmbeddingUnavailable}
	}
	return []error{rememberapp.ErrRememberEmbeddingUnavailable, e.cause}
}
