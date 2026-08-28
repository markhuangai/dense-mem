package serverapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
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

func newSearchRepairService(
	search *repository.SearchRepositoryImpl,
	provider embedding.EmbeddingProviderInterface,
	appConfig service.AppConfigService,
	logger observability.LogProvider,
	metrics observability.DiscoverabilityMetrics,
	providerTimeout time.Duration,
	distributedCoordinationRequired bool,
) service.SearchRepairService {
	hostname, _ := os.Hostname()
	return service.NewSearchRepairService(service.SearchRepairDependencies{
		Repository:                      search,
		Executor:                        semanticwrite.NewExecutor(searchRepairBatchProvider{provider: provider}),
		AppConfig:                       appConfig,
		Logger:                          logger,
		Metrics:                         metrics,
		WorkerID:                        fmt.Sprintf("search-repair-%s-%d", hostname, os.Getpid()),
		ProviderTimeout:                 providerTimeout,
		DistributedCoordinationRequired: distributedCoordinationRequired,
	})
}

type searchRepairBatchProvider struct {
	provider embedding.EmbeddingProviderInterface
}

func (p searchRepairBatchProvider) EmbedBatch(ctx context.Context, texts []string) ([]semanticwrite.IndexedEmbedding, string, error) {
	vectors, model, err := p.provider.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, model, err
	}
	result := make([]semanticwrite.IndexedEmbedding, len(vectors))
	for index, vector := range vectors {
		result[index] = semanticwrite.IndexedEmbedding{Index: index, Vector: vector}
	}
	return result, model, nil
}

func (p searchRepairBatchProvider) ModelName() string { return p.provider.ModelName() }
func (p searchRepairBatchProvider) Dimensions() int   { return p.provider.Dimensions() }
func (p searchRepairBatchProvider) IsAvailable() bool { return p.provider.IsAvailable() }
