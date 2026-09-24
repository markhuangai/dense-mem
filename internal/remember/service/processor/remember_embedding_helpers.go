package processor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func (p *rememberSynchronousProcessor) embedSearchDocumentBatch(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	embeddingModel string,
	documents []repository.SearchDocumentForEmbedding,
) ([]repository.SearchDocumentEmbedding, error) {
	if len(documents) == 0 {
		return []repository.SearchDocumentEmbedding{}, nil
	}
	if len(documents) > 256 {
		return nil, fmt.Errorf("%w: more than 256 search documents", rememberapp.ErrRememberInputBudgetExceeded)
	}
	texts := make([]string, len(documents))
	for i := range documents {
		texts[i] = documents[i].DocumentText
	}
	embedCtx, cancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer cancel()
	embedCtx = observability.WithMetricIdentity(embedCtx, teamID, ownerProfileID)
	embedCtx = observability.WithAIOperation(embedCtx, observability.AIOperationSearchDocumentEmbedding, len(texts))
	if p.embedder == nil || !p.embedder.IsAvailable() {
		return nil, &rememberEmbeddingConfigurationFailure{}
	}
	embeddingModel = strings.TrimSpace(embeddingModel)
	if embeddingModel == "" || strings.TrimSpace(p.embedder.ModelName()) != embeddingModel {
		return nil, fmt.Errorf("%w: configured model does not match the embedding plan", rememberapp.ErrRememberEmbeddingInvalid)
	}
	vectors, model, err := p.embedder.EmbedBatch(embedCtx, texts)
	if err != nil {
		if errors.Is(embedCtx.Err(), context.Canceled) || errors.Is(embedCtx.Err(), rememberapp.ErrRememberRequestCancelled) ||
			errors.Is(err, context.Canceled) || errors.Is(err, rememberapp.ErrRememberRequestCancelled) {
			return nil, fmt.Errorf("%w: embedding phase canceled", rememberapp.ErrRememberRequestCancelled)
		}
		if errors.Is(embedCtx.Err(), context.DeadlineExceeded) || errors.Is(embedCtx.Err(), rememberapp.ErrRememberRequestTimeout) ||
			errors.Is(err, context.DeadlineExceeded) || errors.Is(err, rememberapp.ErrRememberRequestTimeout) {
			return nil, fmt.Errorf("%w: embedding phase exceeded 10 seconds", rememberapp.ErrRememberRequestTimeout)
		}
		return nil, &rememberEmbeddingProviderFailure{cause: err}
	}
	if len(vectors) != len(documents) || strings.TrimSpace(model) != embeddingModel {
		return nil, fmt.Errorf("%w: count or model mismatch", rememberapp.ErrRememberEmbeddingInvalid)
	}
	completed := make([]repository.SearchDocumentEmbedding, len(documents))
	for i, document := range documents {
		if len(vectors[i]) != document.EmbeddingDimensions {
			return nil, fmt.Errorf("%w: dimensions mismatch", rememberapp.ErrRememberEmbeddingInvalid)
		}
		for _, value := range vectors[i] {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("%w: non-finite vector", rememberapp.ErrRememberEmbeddingInvalid)
			}
		}
		completed[i] = repository.SearchDocumentEmbedding{
			SearchDocumentID: document.SearchDocumentID, SourceKind: document.SourceKind, SourceID: document.SourceID,
			SourceVersion: document.SourceVersion, DocumentText: document.DocumentText,
			DocumentHash: document.DocumentHash, StoredDocumentHash: document.StoredDocumentHash,
			ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion: document.DocumentVersion, EmbeddingContractID: document.EmbeddingContractID,
			EmbeddingDimensions: document.EmbeddingDimensions, Embedding: vectors[i], SpaceID: document.SpaceID,
			SpaceGeneration: document.SpaceGeneration,
		}
	}
	return completed, nil
}

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
