package serverapp

import (
	"context"
	"fmt"
	"os"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/embeddingservice"
)

func searchConvergenceHealthCheck(search *repository.SearchRepositoryImpl) func(context.Context) error {
	return func(ctx context.Context) error {
		convergence, err := search.GetSearchConvergence(ctx, repository.SearchConvergenceInput{})
		if err != nil {
			return err
		}
		if convergence != nil && convergence.Status != "converged" {
			return fmt.Errorf("search convergence is %s", convergence.Status)
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
