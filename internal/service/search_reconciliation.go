package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	searchReconciliationDocumentLimit = 256
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

// SearchReconciliationEmbeddingProvider is the provider port consumed by the
// document-repair application service. The concrete provider remains owned by
// the composition root and is injected through this boundary.
type SearchReconciliationEmbeddingProvider interface {
	EmbedBatch(context.Context, []string) ([][]float32, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}

type SearchReconciliationDependencies struct {
	Repository      repository.SearchReconciliationRepository
	Provider        SearchReconciliationEmbeddingProvider
	Now             func() time.Time
	ProviderTimeout time.Duration
}

type searchReconciliationService struct {
	repository      repository.SearchReconciliationRepository
	provider        SearchReconciliationEmbeddingProvider
	now             func() time.Time
	providerTimeout time.Duration
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
	return &searchReconciliationService{
		repository: deps.Repository, provider: deps.Provider,
		now: now, providerTimeout: timeout,
	}
}

func (s *searchReconciliationService) Run(ctx context.Context) (SearchReconciliationResult, error) {
	result := SearchReconciliationResult{}
	if s == nil || s.repository == nil || s.provider == nil {
		return result, fmt.Errorf("%w: service unavailable", ErrSearchReconciliationFailed)
	}
	contract, err := s.repository.GetActiveSearchContract(ctx)
	if err != nil {
		return result, fmt.Errorf("%w: active contract unavailable", ErrSearchReconciliationFailed)
	}
	now := s.now().UTC()
	run, claimed, err := s.repository.ReserveSearchReconciliationRun(ctx, repository.SearchReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Now:                 now,
	})
	if err != nil {
		return result, fmt.Errorf("%w: run reservation failed", ErrSearchReconciliationFailed)
	}
	if !claimed || run == nil {
		result.Status = "skipped"
		result.Skipped = true
		return result, nil
	}
	result.RunID = run.RunID
	result.Status = "running"

	documents, err := s.repository.SelectSearchReconciliationDocuments(ctx, repository.SearchReconciliationSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit:               searchReconciliationDocumentLimit,
	})
	if err != nil {
		return s.fail(ctx, result, 0, 0, "reconciliation_selection_failed", err)
	}
	result.SelectedCount = int64(len(documents))
	if len(documents) == 0 {
		return s.finish(ctx, result, "completed", "")
	}

	texts, hashes, err := reconciliationBatch(documents)
	if err != nil {
		return s.fail(ctx, result, result.SelectedCount, 0, "reconciliation_snapshot_invalid", err)
	}
	var vectors [][]float32
	if len(texts) > 0 {
		if !s.provider.IsAvailable() {
			return s.fail(ctx, result, result.SelectedCount, 0, "embedding_unavailable", nil)
		}
		if model := strings.TrimSpace(s.provider.ModelName()); model != contract.EmbeddingModel {
			return s.fail(ctx, result, result.SelectedCount, 0, "embedding_contract_mismatch", nil)
		}
		if dimensions := s.provider.Dimensions(); dimensions != 0 && dimensions != contract.EmbeddingDimensions {
			return s.fail(ctx, result, result.SelectedCount, 0, "embedding_contract_mismatch", nil)
		}

		embedCtx, cancel := context.WithTimeout(ctx, s.providerTimeout)
		// One reconciliation batch can span teams, so it must not inherit a document's tenant identity.
		embedCtx = observability.WithMetricIdentity(embedCtx, "", "")
		embedCtx = observability.WithAIOperation(embedCtx, observability.AIOperationSearchDocumentEmbedding, len(texts))
		var model string
		var providerErr error
		vectors, model, providerErr = s.provider.EmbedBatch(embedCtx, texts)
		providerCtxErr := embedCtx.Err()
		cancel()
		if providerErr != nil {
			if errors.Is(providerCtxErr, context.DeadlineExceeded) || errors.Is(providerErr, context.DeadlineExceeded) {
				return s.fail(ctx, result, result.SelectedCount, 0, "embedding_timeout", providerErr)
			}
			if errors.Is(providerCtxErr, context.Canceled) || errors.Is(providerErr, context.Canceled) {
				return s.fail(ctx, result, result.SelectedCount, 0, "embedding_cancelled", providerErr)
			}
			return s.fail(ctx, result, result.SelectedCount, 0, "embedding_provider_failed", providerErr)
		}
		if strings.TrimSpace(model) != contract.EmbeddingModel || len(vectors) != len(texts) {
			return s.fail(ctx, result, result.SelectedCount, 0, "embedding_response_invalid", nil)
		}
		for _, vector := range vectors {
			if len(vector) != contract.EmbeddingDimensions {
				return s.fail(ctx, result, result.SelectedCount, 0, "embedding_response_invalid", nil)
			}
			for _, value := range vector {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return s.fail(ctx, result, result.SelectedCount, 0, "embedding_response_invalid", nil)
				}
			}
		}
		result.EmbeddedCount = int64(len(vectors))
	}

	byHash := make(map[string][]float32, len(hashes))
	for index, hash := range hashes {
		byHash[hash] = vectors[index]
	}
	embeddings := make([]repository.SearchDocumentEmbedding, 0, len(documents))
	for _, document := range documents {
		embeddings = append(embeddings, repository.SearchDocumentEmbedding{
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
	applyResult, err := s.repository.CompleteSearchReconciliationDocuments(ctx, repository.ApplySearchReconciliationInput{
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

func reconciliationBatch(documents []repository.SearchDocumentForEmbedding) ([]string, []string, error) {
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
	err := s.repository.FinishSearchReconciliationRun(finishCtx, repository.FinishSearchReconciliationRunInput{
		RunID:         result.RunID,
		Status:        status,
		SelectedCount: result.SelectedCount,
		EmbeddedCount: result.EmbeddedCount,
		UpdatedCount:  result.UpdatedCount,
		DriftedCount:  result.DriftedCount,
		LastError:     lastError,
	})
	if err != nil {
		return result, fmt.Errorf("%w: run finalization failed", ErrSearchReconciliationFailed)
	}
	result.Status = status
	result.ErrorCode = lastError
	if status == "failed" {
		return result, fmt.Errorf("%w: %s", ErrSearchReconciliationFailed, lastError)
	}
	return result, nil
}

func (s *searchReconciliationService) fail(ctx context.Context, result SearchReconciliationResult, selected, embedded int64, code string, _ error) (SearchReconciliationResult, error) {
	result.SelectedCount = selected
	result.EmbeddedCount = embedded
	result.ErrorCode = code
	return s.finish(ctx, result, "failed", code)
}
