package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type recallApplicationDependencies struct {
	Search   recallcontract.SearchRepository
	Provider embeddingcontract.EmbeddingProviderInterface

	Communities recall.RecallCommunityRepository

	OntologyConfig recall.RecallOntologyConfigProvider
	Metrics        observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) recall.RecallService {
	return recall.NewRecallService(recall.RecallDependencies{
		Search:         deps.Search,
		Provider:       deps.Provider,
		Communities:    deps.Communities,
		OntologyConfig: deps.OntologyConfig,
		Metrics:        deps.Metrics,
	})
}
