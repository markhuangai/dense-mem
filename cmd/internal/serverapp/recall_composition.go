package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type recallApplicationDependencies struct {
	Search          recallcontract.SearchRepository
	Provider        embeddingcontract.EmbeddingProviderInterface
	Hypotheses      recall.RecallHypothesisRepository
	Communities     recall.RecallCommunityRepository
	CommunityConfig recall.RecallCommunityConfigProvider
	Metrics         observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) recall.RecallService {
	return recall.NewRecallService(recall.RecallDependencies{
		Search:          deps.Search,
		Provider:        deps.Provider,
		Hypotheses:      deps.Hypotheses,
		Communities:     deps.Communities,
		CommunityConfig: deps.CommunityConfig,
		Metrics:         deps.Metrics,
	})
}
