package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

const (
	searchReconciliationDocumentLimit = semanticwrite.MaxDocuments
	searchReconciliationProviderCap   = 10 * time.Second
	searchReconciliationFinalizeCap   = 5 * time.Second
)

var ErrSearchReconciliationFailed = errors.New("search reconciliation failed")

type SearchReconciliationResult struct {
	RunID         string
	Status        string
	SelectedCount int64
	EmbeddedCount int64
	UpdatedCount  int64
	DriftedCount  int64
	Skipped       bool
	ErrorCode     string
}

type SearchReconciliationService interface {
	Run(context.Context) (SearchReconciliationResult, error)
}

// The provider-independent embedding contract is the only provider boundary
// consumed by reconciliation. Concrete providers remain composition-owned.

type SearchReconciliationDependencies struct {
	Repository      searchcontract.SearchRepository
	Projection      knowledgecontract.SearchProjectionRepository
	Executor        *semanticwrite.Executor
	Logger          observability.LogProvider
	Now             func() time.Time
	ProviderTimeout time.Duration
	BatchLimit      int
}

type searchReconciliationService struct {
	repository      searchcontract.SearchRepository
	projection      knowledgecontract.SearchProjectionRepository
	executor        *semanticwrite.Executor
	logger          observability.LogProvider
	now             func() time.Time
	providerTimeout time.Duration
	batchLimit      int
}

func NewSearchReconciliationService(deps SearchReconciliationDependencies) SearchReconciliationService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := deps.ProviderTimeout
	if timeout <= 0 || timeout > searchReconciliationProviderCap {
		timeout = searchReconciliationProviderCap
	}
	limit := deps.BatchLimit
	if limit <= 0 || limit > searchReconciliationDocumentLimit {
		limit = searchReconciliationDocumentLimit
	}
	return &searchReconciliationService{
		repository: deps.Repository, projection: deps.Projection, executor: deps.Executor, logger: deps.Logger,
		now: now, providerTimeout: timeout, batchLimit: limit,
	}
}

func (s *searchReconciliationService) Run(ctx context.Context) (result SearchReconciliationResult, runErr error) {
	started := time.Now()
	var fence semanticwrite.Fence
	var sources []map[string]any
	defer func() {
		if runErr == nil || s == nil || s.logger == nil {
			return
		}
		cause := runErr
		var failure *reconciliationFailure
		if errors.As(runErr, &failure) && failure.cause != nil {
			cause = failure.cause
		}
		metadata := embeddingcontract.ClassifyFailure(cause)
		if errors.Is(cause, semanticwrite.ErrProviderTimeout) {
			metadata = embeddingcontract.ClassifyFailure(context.DeadlineExceeded)
		}
		attrs := []observability.LogAttr{
			observability.String("run_id", result.RunID), observability.String("error_code", result.ErrorCode),
			observability.String("failure_class", metadata.Class), observability.String("failure_code", metadata.Code),
			{Key: "embedding_fence", Value: fence.LogFields()}, {Key: "sources", Value: sources},
			{Key: "selected_count", Value: result.SelectedCount}, {Key: "embedded_count", Value: result.EmbeddedCount},
			{Key: "updated_count", Value: result.UpdatedCount}, {Key: "duration_ms", Value: time.Since(started).Milliseconds()},
			observability.String("outcome", "failed"),
		}
		if contextual, ok := s.logger.(observability.ContextLogProvider); ok {
			contextual.ErrorContext(ctx, "search_reconciliation_failed", cause, attrs...)
		} else {
			s.logger.Error("search_reconciliation_failed", errors.New("[diagnostic unavailable]"), attrs...)
		}
	}()
	if s == nil || s.repository == nil || s.projection == nil {
		return result, fmt.Errorf("%w: service unavailable", ErrSearchReconciliationFailed)
	}
	contract, err := s.repository.GetActiveSearchContract(ctx)
	if err != nil {
		return result, &reconciliationFailure{code: "active contract unavailable", cause: err}
	}
	fence = semanticwrite.Fence{Model: contract.EmbeddingModel, Dimensions: contract.EmbeddingDimensions, EmbeddingContractID: contract.EmbeddingContractID, SearchGenerationID: contract.SearchIndexGenerationID, SearchGenerationVersion: int64(contract.IndexGeneration)}
	now := s.now().UTC()
	run, claimed, err := s.projection.ReserveSearchReconciliationRun(ctx, knowledgecontract.SearchReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Now:                 now,
	})
	if err != nil {
		return result, &reconciliationFailure{code: "run reservation failed", cause: err}
	}
	if !claimed || run == nil {
		result.Status = "skipped"
		result.Skipped = true
		return result, nil
	}
	result.RunID = run.RunID
	result.Status = "running"
	ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{RunID: run.RunID})

	documents, err := s.projection.SelectSearchReconciliationDocuments(ctx, knowledgecontract.SearchReconciliationSelectionInput{
		RunID:               run.RunID,
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit:               s.batchLimit,
	})
	if err != nil {
		return s.fail(ctx, result, 0, 0, "reconciliation_selection_failed", err)
	}
	result.SelectedCount = int64(len(documents))
	for _, document := range documents {
		sources = append(sources, map[string]any{"team_id": document.TeamID, "owner_profile_id": document.OwnerProfileID, "source_kind": document.SourceKind, "source_id": document.SourceID, "source_version": document.SourceVersion, "search_document_id": document.SearchDocumentID, "document_hash": document.DocumentHash, "document_version": document.DocumentVersion, "space_id": document.SpaceID, "space_generation": document.SpaceGeneration, "retired": document.Retired})
	}
	if contextual, ok := s.logger.(observability.ContextLogProvider); ok {
		contextual.DebugContext(ctx, "search_reconciliation_planned", observability.String("run_id", result.RunID), observability.LogAttr{Key: "embedding_fence", Value: fence.LogFields()}, observability.LogAttr{Key: "sources", Value: sources})
	}
	if len(documents) == 0 {
		return s.finish(ctx, result, "completed", "")
	}

	texts, hashes, err := reconciliationBatch(documents)
	if err != nil {
		return s.fail(ctx, result, result.SelectedCount, 0, "reconciliation_snapshot_invalid", err)
	}
	byHash := make(map[string][]float32, len(hashes))
	if len(texts) > 0 {
		planned := make([]semanticwrite.Document, len(texts))
		for index, text := range texts {
			planned[index] = semanticwrite.Document{Hash: hashes[index], Text: text}
		}
		embedded, providerErr := s.executor.Execute(observability.WithAIOperation(ctx, observability.AIOperationSearchDocumentEmbedding, len(texts)), semanticwrite.Plan{Documents: planned, Fence: fence, Timeout: s.providerTimeout})
		if providerErr != nil {
			code := "embedding_provider_failed"
			var executionError *semanticwrite.ExecutionError
			hasExecutionError := errors.As(providerErr, &executionError)
			switch {
			case errors.Is(providerErr, context.DeadlineExceeded), (errors.Is(providerErr, semanticwrite.ErrProviderTimeout) && !hasExecutionError):
				code = "embedding_timeout"
			case errors.Is(providerErr, context.Canceled):
				code = "embedding_cancelled"
			case hasExecutionError:
				switch executionError.Reason {
				case semanticwrite.ReasonProviderUnavailable:
					code = "embedding_unavailable"
				case semanticwrite.ReasonContractMismatch:
					code = "embedding_contract_mismatch"
				case semanticwrite.ReasonInvalidResponse:
					code = "embedding_response_invalid"
				}
				if executionError.Cause != nil {
					providerErr = executionError.Cause
				}
			case errors.Is(providerErr, semanticwrite.ErrInvalidPlan):
				code = "reconciliation_snapshot_invalid"
			}
			return s.fail(ctx, result, result.SelectedCount, 0, code, providerErr)
		}
		for _, embedding := range embedded.Embeddings {
			byHash[embedding.DocumentHash] = embedding.Vector
		}
		result.EmbeddedCount = int64(len(embedded.Embeddings))
	}

	embeddings := make([]SearchDocumentEmbedding, 0, len(documents))
	for _, document := range documents {
		embeddings = append(embeddings, SearchDocumentEmbedding{
			TeamID:                 document.TeamID,
			SearchDocumentID:       document.SearchDocumentID,
			OwnerProfileID:         document.OwnerProfileID,
			SourceKind:             document.SourceKind,
			SourceID:               document.SourceID,
			DocumentText:           document.DocumentText,
			DocumentHash:           document.DocumentHash,
			StoredDocumentHash:     document.StoredDocumentHash,
			SourceVersion:          document.SourceVersion,
			ProjectionFormat:       document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion:        document.DocumentVersion,
			EmbeddingContractID:    document.EmbeddingContractID,
			EmbeddingDimensions:    document.EmbeddingDimensions,
			Embedding:              byHash[document.DocumentHash],
			SpaceID:                document.SpaceID,
			SpaceGeneration:        document.SpaceGeneration,
			Retired:                document.Retired,
		})
	}
	applyResult, err := s.projection.CompleteSearchReconciliationDocuments(ctx, knowledgecontract.ApplySearchReconciliationInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Documents:           embeddings,
	})
	if err != nil {
		return s.fail(ctx, result, result.SelectedCount, result.EmbeddedCount, "reconciliation_commit_failed", err)
	}
	result.UpdatedCount = applyResult.UpdatedCount
	result.DriftedCount = applyResult.RemainingDriftedCount
	return s.finish(ctx, result, "completed", "")
}

func reconciliationBatch(documents []SearchDocumentForEmbedding) ([]string, []string, error) {
	texts := make([]string, 0, len(documents))
	hashes := make([]string, 0, len(documents))
	byHash := make(map[string]string, len(documents))
	for _, document := range documents {
		if document.Retired {
			continue
		}
		hash := strings.TrimSpace(document.DocumentHash)
		text := strings.TrimSpace(document.DocumentText)
		if hash == "" || text == "" {
			return nil, nil, errors.New("reconciliation document is missing text or hash")
		}
		if previous, ok := byHash[hash]; ok {
			if previous != text {
				return nil, nil, errors.New("reconciliation hash maps to conflicting document text")
			}
			continue
		}
		byHash[hash] = text
		hashes = append(hashes, hash)
		texts = append(texts, text)
	}
	if len(texts) > searchReconciliationDocumentLimit {
		return nil, nil, errors.New("reconciliation document limit exceeded")
	}
	return texts, hashes, nil
}

func (s *searchReconciliationService) finish(ctx context.Context, result SearchReconciliationResult, status, lastError string) (SearchReconciliationResult, error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchReconciliationFinalizeCap)
	defer cancel()
	err := s.projection.FinishSearchReconciliationRun(finishCtx, knowledgecontract.FinishSearchReconciliationRunInput{
		RunID:         result.RunID,
		Status:        status,
		SelectedCount: result.SelectedCount,
		EmbeddedCount: result.EmbeddedCount,
		UpdatedCount:  result.UpdatedCount,
		DriftedCount:  result.DriftedCount,
		LastError:     lastError,
	})
	if err != nil {
		return result, &reconciliationFailure{code: "run finalization failed", cause: err}
	}
	result.Status = status
	result.ErrorCode = lastError
	return result, nil
}

func (s *searchReconciliationService) fail(ctx context.Context, result SearchReconciliationResult, selected, embedded int64, code string, cause error) (SearchReconciliationResult, error) {
	result.SelectedCount = selected
	result.EmbeddedCount = embedded
	result.ErrorCode = code
	finished, err := s.finish(ctx, result, "failed", code)
	if err != nil {
		cause = errors.Join(cause, err)
	}
	return finished, &reconciliationFailure{code: code, cause: cause}
}

type reconciliationFailure struct {
	code  string
	cause error
}

func (e *reconciliationFailure) Error() string {
	return ErrSearchReconciliationFailed.Error() + ": " + e.code
}
func (e *reconciliationFailure) Unwrap() []error {
	return []error{ErrSearchReconciliationFailed, e.cause}
}
