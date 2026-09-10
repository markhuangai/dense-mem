package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
)

type recallApplicationDependencies struct {
	Search          recallpostgres.Source
	Provider        embeddingcontract.EmbeddingProviderInterface
	Hypotheses      recall.RecallHypothesisRepository
	Communities     recall.RecallCommunityRepository
	CommunityConfig recall.RecallCommunityConfigProvider
	Metrics         observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) recall.RecallService {
	var search recall.RecallSearchRepository
	if deps.Search != nil {
		search = recallpostgres.NewStoreFromSource(deps.Search)
	}
	return recall.NewRecallService(recall.RecallDependencies{
		Search:          search,
		Provider:        deps.Provider,
		Hypotheses:      deps.Hypotheses,
		Communities:     deps.Communities,
		CommunityConfig: deps.CommunityConfig,
		Metrics:         deps.Metrics,
	})
}
