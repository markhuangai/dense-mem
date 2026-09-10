package serverapp

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type searchApplication struct {
	Repository        *repository.SearchRepositoryImpl
	Contract          *repository.EnsureActiveSearchContractResult
	EmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	RetryEmbedding    embeddingcontract.EmbeddingProviderInterface
	Convergence       service.SearchConvergenceReader
	Reconciliation    searchapp.SearchReconciliationService
}

func buildSearchRepositoryApplication(
	startupCtx context.Context,
	cfg config.Config,
	db *postgres.DB,
	rls *postgres.RLS,
) (*repository.SearchRepositoryImpl, *repository.EnsureActiveSearchContractResult, error) {
	searchRepo := repository.NewSearchRepository(db.GetDB(), rls)
	var searchOwner *searchpostgres.Store
	if searchRepo != nil {
		searchOwner = searchRepo.SearchMaintenanceStore()
	}
	if searchOwner == nil {
		return nil, nil, errors.New("search: native maintenance store is required")
	}
	contract, err := searchOwner.EnsureActiveSearchContract(startupCtx, searchapp.EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
	if err != nil {
		return nil, nil, err
	}
	return searchRepo, &repository.EnsureActiveSearchContractResult{
		Contract: contract.Contract, CreatedContract: contract.CreatedContract,
		CreatedGeneration: contract.CreatedGeneration, CreatedPhysicalIndex: contract.CreatedPhysicalIndex,
	}, nil
}

func buildSearchProviders(
	cfg config.Config,
	searchRepo *repository.SearchRepositoryImpl,
	contract *repository.EnsureActiveSearchContractResult,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) *searchApplication {
	openaiProvider := embedding.NewOpenAIEmbeddingProvider(&cfg, nil)
	openaiProvider.SetMetrics(metrics)
	retryEmbedder := embedding.NewRetryEmbeddingProviderWithKey(openaiProvider, logger, cfg.GetAIAPIKey())
	retryEmbedder.SetMetrics(metrics)
	var searchOwner *searchpostgres.Store
	if searchRepo != nil {
		searchOwner = searchRepo.SearchMaintenanceStore()
	}
	return &searchApplication{
		Repository:        searchRepo,
		Contract:          contract,
		EmbeddingProvider: openaiProvider,
		RetryEmbedding:    retryEmbedder,
		Convergence:       service.NewSearchConvergenceCompatibility(searchapp.NewSearchConvergenceService(searchOwner)),
		Reconciliation: searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{
			Repository:      searchOwner,
			Provider:        openaiProvider,
			ProviderTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		}),
	}
}
