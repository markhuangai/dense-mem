package serverapp

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type searchApplication struct {
	Repository        *repository.SearchRepositoryImpl
	Contract          *repository.EnsureActiveSearchContractResult
	EmbeddingProvider embedding.EmbeddingProviderInterface
	RetryEmbedding    embedding.EmbeddingProviderInterface
	Convergence       service.SearchConvergenceReader
	Reconciliation    service.SearchReconciliationService
}

func buildSearchRepositoryApplication(
	startupCtx context.Context,
	cfg config.Config,
	db *postgres.DB,
	rls *postgres.RLS,
) (*repository.SearchRepositoryImpl, *repository.EnsureActiveSearchContractResult, error) {
	searchRepo := repository.NewSearchRepository(db.GetDB(), rls)
	contract, err := searchRepo.EnsureActiveSearchContract(startupCtx, repository.EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
	if err != nil {
		return nil, nil, err
	}
	return searchRepo, contract, nil
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
	return &searchApplication{
		Repository:        searchRepo,
		Contract:          contract,
		EmbeddingProvider: openaiProvider,
		RetryEmbedding:    retryEmbedder,
		Convergence:       service.NewSearchConvergenceService(searchRepo),
		Reconciliation: service.NewSearchReconciliationService(service.SearchReconciliationDependencies{
			Repository:      searchRepo,
			Provider:        openaiProvider,
			ProviderTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		}),
	}
}
