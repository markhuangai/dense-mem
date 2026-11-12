package serverapp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type searchApplication struct {
	Repository        searchcontract.SearchRepository
	Contract          *searchmaintenance.EnsureActiveSearchContractResult
	EmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	RetryEmbedding    embeddingcontract.EmbeddingProviderInterface
	Convergence       searchapp.SearchConvergenceReader
	Reconciliation    searchapp.SearchReconciliationService
}

func buildSearchRepositoryApplication(
	startupCtx context.Context,
	cfg config.Config,
	db *postgres.DB,
	rls *postgres.RLS,
	knowledgeOwner *knowledgepostgres.Store,
) (*searchpostgres.Store, *searchmaintenance.EnsureActiveSearchContractResult, error) {
	searchOwner := searchpostgres.NewStore(db.GetDB(), rls)
	if searchOwner == nil || knowledgeOwner == nil {
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
	return searchOwner, contract, nil
}

func buildSearchProviders(
	cfg config.Config,
	searchOwner *searchpostgres.Store,
	contract *searchmaintenance.EnsureActiveSearchContractResult,
	projection knowledgecontract.SearchProjectionRepository,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
	protector observability.DiagnosticProtector,
) *searchApplication {
	client := observability.DiagnosticHTTPClient(&http.Client{Timeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second}, rootDiagnosticLogger(logger), protector)
	openaiProvider := embedding.NewOpenAIEmbeddingProvider(&cfg, client)
	openaiProvider.SetMetrics(metrics)
	retryEmbedder := embedding.NewRetryEmbeddingProviderWithKey(openaiProvider, logger, cfg.GetAIAPIKey())
	retryEmbedder.SetMetrics(metrics)
	return &searchApplication{
		Repository:        searchOwner,
		Contract:          contract,
		EmbeddingProvider: openaiProvider,
		RetryEmbedding:    retryEmbedder,
		Convergence:       searchapp.NewSearchConvergenceService(searchOwner, projection),
		Reconciliation: searchapp.NewSearchReconciliationService(searchapp.SearchReconciliationDependencies{
			Repository:      searchOwner,
			Projection:      projection,
			Executor:        semanticwrite.NewProviderExecutor(openaiProvider),
			Logger:          logger,
			ProviderTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
			BatchLimit:      config.AIEmbeddingMaxBatchItems(&cfg),
		}),
	}
}
