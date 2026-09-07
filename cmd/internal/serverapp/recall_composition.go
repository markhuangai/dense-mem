package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type recallApplicationDependencies struct {
	Search          memoryservice.RecallSearchRepository
	Provider        embedding.EmbeddingProviderInterface
	Hypotheses      memoryservice.RecallHypothesisRepository
	Communities     memoryservice.RecallCommunityRepository
	CommunityConfig memoryservice.RecallCommunityConfigProvider
	Metrics         observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) memoryservice.RecallService {
	return memoryservice.NewRecallService(memoryservice.RecallDependencies{
		Search:          deps.Search,
		Provider:        deps.Provider,
		Hypotheses:      deps.Hypotheses,
		Communities:     deps.Communities,
		CommunityConfig: deps.CommunityConfig,
		Metrics:         deps.Metrics,
	})
}
