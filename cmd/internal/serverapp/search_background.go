package serverapp

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/embeddingservice"
)

var errSearchConvergenceQueryFailed = errors.New("search convergence query failed")

type searchConvergenceHealthReader interface {
	CheckSearchConvergence(context.Context) error
}

func searchConvergenceHealthCheck(search searchConvergenceHealthReader, logger observability.LogProvider) func(context.Context) error {
	return func(ctx context.Context) error {
		err := search.CheckSearchConvergence(ctx)
		if err != nil {
			if errors.Is(err, repository.ErrSearchConvergenceAttentionRequired) {
				return err
			}
			if logger != nil {
				logger.Warn("search_convergence_health_query_failed", observability.String("error_code", "search_convergence_query_failed"))
			}
			return errSearchConvergenceQueryFailed
		}
		return nil
	}
}

func newEmbeddingReconciliationService(
	search *repository.SearchRepositoryImpl,
	provider embedding.EmbeddingProviderInterface,
	appConfig service.AppConfigService,
	logger observability.LogProvider,
	metrics observability.DiscoverabilityMetrics,
) embeddingservice.EmbeddingReconciliationService {
	hostname, _ := os.Hostname()
	return embeddingservice.NewEmbeddingReconciliationService(embeddingservice.EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: search, Provider: provider, AppConfig: appConfig,
		Logger: logger, Metrics: metrics, WorkerID: fmt.Sprintf("embedding-reconciliation-%s-%d", hostname, os.Getpid()),
	})
}
