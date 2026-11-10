package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type recallApplicationDependencies struct {
	Decisions        modelprovider.DecisionProvider
	DecisionSettings modelprovider.DecisionSettings
	Logger           observability.LogProvider
	Search           recallcontract.SearchRepository
	Provider         embeddingcontract.EmbeddingProviderInterface

	OntologyConfig recall.RecallOntologyConfigProvider
	Metrics        observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) recall.RecallService {
	return recall.NewRecallService(recall.RecallDependencies{
		Decisions: deps.Decisions, DecisionSettings: deps.DecisionSettings, Logger: deps.Logger,
		Search:         deps.Search,
		Provider:       deps.Provider,
		OntologyConfig: deps.OntologyConfig,
		Metrics:        deps.Metrics,
	})
}
