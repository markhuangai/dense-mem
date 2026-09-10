package service

import (
	"context"
	"time"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/repository"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
)

// Search reconciliation names remain compatibility aliases while callers
// migrate to the search capability application package.
type SearchReconciliationResult = searchapp.SearchReconciliationResult
type SearchReconciliationService = searchapp.SearchReconciliationService
type SearchReconciliationEmbeddingProvider = embeddingcontract.EmbeddingProviderInterface

type SearchReconciliationDependencies struct {
	Repository      repository.SearchReconciliationRepository
	Provider        SearchReconciliationEmbeddingProvider
	Now             func() time.Time
	ProviderTimeout time.Duration
}

var ErrSearchReconciliationFailed = searchapp.ErrSearchReconciliationFailed

func NewSearchReconciliationService(deps SearchReconciliationDependencies) SearchReconciliationService {
	var repositoryPort searchapp.SearchReconciliationRepository
	if deps.Repository != nil {
		repositoryPort = legacySearchReconciliationRepository{delegate: deps.Repository}
	}
	return searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{
		Repository:      repositoryPort,
		Provider:        deps.Provider,
		Now:             deps.Now,
		ProviderTimeout: deps.ProviderTimeout,
	})
}

type legacySearchReconciliationRepository struct {
	delegate repository.SearchReconciliationRepository
}

func (r legacySearchReconciliationRepository) GetActiveSearchContract(ctx context.Context) (*searchcontract.ActiveSearchContract, error) {
	return r.delegate.GetActiveSearchContract(ctx)
}

func (r legacySearchReconciliationRepository) CheckSearchReadiness(ctx context.Context) (*searchcontract.SearchReadiness, error) {
	return r.delegate.CheckSearchReadiness(ctx)
}

func (r legacySearchReconciliationRepository) SearchFullText(ctx context.Context, input searchcontract.FullTextSearchInput) ([]searchcontract.SearchHit, error) {
	return r.delegate.SearchFullText(ctx, input)
}

func (r legacySearchReconciliationRepository) SearchExactVector(ctx context.Context, input searchcontract.ExactVectorSearchInput) ([]searchcontract.SearchHit, error) {
	return r.delegate.SearchExactVector(ctx, input)
}

func (r legacySearchReconciliationRepository) ReserveSearchReconciliationRun(ctx context.Context, input searchmaintenance.SearchReconciliationRunInput) (*searchmaintenance.SearchReconciliationRun, bool, error) {
	run, claimed, err := r.delegate.ReserveSearchReconciliationRun(ctx, repository.SearchReconciliationRunInput{
		EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions,
		Now: input.Now, StaleAfter: input.StaleAfter,
	})
	return nativeSearchReconciliationRun(run), claimed, err
}

func (r legacySearchReconciliationRepository) SelectSearchReconciliationDocuments(ctx context.Context, input searchmaintenance.SearchReconciliationSelectionInput) ([]searchmaintenance.SearchDocumentForEmbedding, error) {
	documents, err := r.delegate.SelectSearchReconciliationDocuments(ctx, repository.SearchReconciliationSelectionInput{
		RunID: input.RunID, EmbeddingContractID: input.EmbeddingContractID,
		EmbeddingDimensions: input.EmbeddingDimensions, Limit: input.Limit,
	})
	return nativeSearchDocuments(documents), err
}

func (r legacySearchReconciliationRepository) CompleteSearchReconciliationDocuments(ctx context.Context, input searchmaintenance.ApplySearchReconciliationInput) (*searchmaintenance.SearchReconciliationApplyResult, error) {
	result, err := r.delegate.CompleteSearchReconciliationDocuments(ctx, repository.ApplySearchReconciliationInput{
		EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions,
		Documents: legacySearchDocumentEmbeddings(input.Documents),
	})
	if result == nil {
		return nil, err
	}
	return &searchmaintenance.SearchReconciliationApplyResult{UpdatedCount: result.UpdatedCount, SkippedCount: result.SkippedCount, RemainingDriftedCount: result.RemainingDriftedCount}, err
}

func (r legacySearchReconciliationRepository) FinishSearchReconciliationRun(ctx context.Context, input searchmaintenance.FinishSearchReconciliationRunInput) error {
	return r.delegate.FinishSearchReconciliationRun(ctx, repository.FinishSearchReconciliationRunInput{
		RunID: input.RunID, Status: input.Status, SelectedCount: input.SelectedCount,
		EmbeddedCount: input.EmbeddedCount, UpdatedCount: input.UpdatedCount,
		DriftedCount: input.DriftedCount, LastError: input.LastError,
	})
}

func nativeSearchReconciliationRun(value *repository.SearchReconciliationRun) *searchmaintenance.SearchReconciliationRun {
	if value == nil {
		return nil
	}
	return &searchmaintenance.SearchReconciliationRun{
		RunID: value.RunID, LocalRunDate: value.LocalRunDate, Status: value.Status,
		SelectedCount: value.SelectedCount, EmbeddedCount: value.EmbeddedCount,
		UpdatedCount: value.UpdatedCount, DriftedCount: value.DriftedCount,
		LastError: value.LastError, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt,
		UpdatedAt: value.UpdatedAt,
	}
}

func nativeSearchDocument(value repository.SearchDocumentForEmbedding) searchmaintenance.SearchDocumentForEmbedding {
	return searchmaintenance.SearchDocumentForEmbedding{
		SearchDocumentResult: searchmaintenance.SearchDocumentResult{
			TeamID: value.TeamID, SearchDocumentID: value.SearchDocumentID, OwnerProfileID: value.OwnerProfileID,
			SourceKind: value.SourceKind, SourceID: value.SourceID, SourceVersion: value.SourceVersion,
			ProjectionFormat: value.ProjectionFormat, ProjectionGenerationID: value.ProjectionGenerationID,
			DocumentVersion: value.DocumentVersion, EmbeddingContractID: value.EmbeddingContractID,
			EmbeddingDimensions: value.EmbeddingDimensions, SearchState: value.SearchState,
			SpaceID: value.SpaceID, SpaceGeneration: value.SpaceGeneration,
		},
		DocumentText: value.DocumentText,
		DocumentHash: value.DocumentHash, StoredDocumentHash: value.StoredDocumentHash, Retired: value.Retired,
	}
}

func nativeSearchDocuments(values []repository.SearchDocumentForEmbedding) []searchmaintenance.SearchDocumentForEmbedding {
	if values == nil {
		return nil
	}
	result := make([]searchmaintenance.SearchDocumentForEmbedding, len(values))
	for index, value := range values {
		result[index] = nativeSearchDocument(value)
	}
	return result
}

func legacySearchDocumentEmbedding(value searchmaintenance.SearchDocumentEmbedding) repository.SearchDocumentEmbedding {
	return repository.SearchDocumentEmbedding{
		TeamID: value.TeamID, SearchDocumentID: value.SearchDocumentID, OwnerProfileID: value.OwnerProfileID,
		SourceKind: value.SourceKind, SourceID: value.SourceID, DocumentText: value.DocumentText,
		DocumentHash: value.DocumentHash, StoredDocumentHash: value.StoredDocumentHash,
		SourceVersion: value.SourceVersion, ProjectionFormat: value.ProjectionFormat,
		ProjectionGenerationID: value.ProjectionGenerationID, DocumentVersion: value.DocumentVersion,
		EmbeddingContractID: value.EmbeddingContractID, EmbeddingDimensions: value.EmbeddingDimensions,
		Embedding: append([]float32(nil), value.Embedding...), SpaceID: value.SpaceID,
		SpaceGeneration: value.SpaceGeneration, Retired: value.Retired,
	}
}

func legacySearchDocumentEmbeddings(values []searchmaintenance.SearchDocumentEmbedding) []repository.SearchDocumentEmbedding {
	result := make([]repository.SearchDocumentEmbedding, len(values))
	for index, value := range values {
		result[index] = legacySearchDocumentEmbedding(value)
	}
	return result
}

var _ searchapp.SearchReconciliationRepository = legacySearchReconciliationRepository{}
