package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func (p *rememberSynchronousProcessor) embedSearchDocumentBatch(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	fence semanticwrite.Fence,
	documents []repository.SearchDocumentForEmbedding,
) ([]repository.SearchDocumentEmbedding, error) {
	if len(documents) == 0 {
		return []repository.SearchDocumentEmbedding{}, nil
	}
	if len(documents) > semanticwrite.MaxDocuments {
		return nil, fmt.Errorf("%w: more than 256 search documents", rememberapp.ErrRememberInputBudgetExceeded)
	}
	planned := make([]semanticwrite.Document, len(documents))
	for i, document := range documents {
		if document.EmbeddingDimensions != fence.Dimensions {
			return nil, fmt.Errorf("%w: document dimensions differ from the plan", rememberapp.ErrRememberEmbeddingInvalid)
		}
		planned[i] = semanticwrite.Document{Hash: document.DocumentHash, Text: document.DocumentText}
	}
	embedCtx, cancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer cancel()
	embedCtx = observability.WithMetricIdentity(embedCtx, teamID, ownerProfileID)
	embedCtx = observability.WithAIOperation(embedCtx, observability.AIOperationSearchDocumentEmbedding, len(documents))
	if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
		contextual.DebugContext(embedCtx, "remember_embedding_planned", observability.LogAttr{Key: "embedding_fence", Value: fence.LogFields()}, observability.Int("document_count", len(documents)))
	}
	result, err := p.embeddingExecutor.Execute(embedCtx, semanticwrite.Plan{Documents: planned, Fence: fence, Timeout: rememberapp.RememberEmbeddingBudget})
	if err != nil {
		var executionError *semanticwrite.ExecutionError
		hasExecutionError := errors.As(err, &executionError)
		if errors.Is(embedCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) || errors.Is(err, rememberapp.ErrRememberRequestCancelled) {
			return nil, fmt.Errorf("%w: embedding phase canceled", rememberapp.ErrRememberRequestCancelled)
		}
		if errors.Is(embedCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.Is(err, semanticwrite.ErrProviderTimeout) && !hasExecutionError) || errors.Is(err, rememberapp.ErrRememberRequestTimeout) {
			return nil, fmt.Errorf("%w: embedding phase exceeded 10 seconds", rememberapp.ErrRememberRequestTimeout)
		}
		if hasExecutionError {
			if executionError.Reason == semanticwrite.ReasonProviderUnavailable {
				return nil, &rememberEmbeddingConfigurationFailure{}
			}
			if executionError.Reason == semanticwrite.ReasonProviderCall {
				return nil, &rememberEmbeddingProviderFailure{cause: executionError.Cause}
			}
		}
		return nil, fmt.Errorf("%w: %w", rememberapp.ErrRememberEmbeddingInvalid, err)
	}
	completed := make([]repository.SearchDocumentEmbedding, len(documents))
	for i, document := range documents {
		completed[i] = repository.SearchDocumentEmbedding{
			SearchDocumentID: document.SearchDocumentID, SourceKind: document.SourceKind, SourceID: document.SourceID,
			SourceVersion: document.SourceVersion, DocumentText: document.DocumentText,
			DocumentHash: document.DocumentHash, StoredDocumentHash: document.StoredDocumentHash,
			ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion: document.DocumentVersion, EmbeddingContractID: document.EmbeddingContractID,
			EmbeddingDimensions: document.EmbeddingDimensions, Embedding: result.Embeddings[i].Vector, SpaceID: document.SpaceID,
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
