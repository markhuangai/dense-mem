package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type recallApplicationDependencies struct {
	ExpansionEnabled      bool
	DisableQueryEmbedding bool
	Decisions             modelprovider.DecisionProvider
	DecisionSettings      modelprovider.DecisionSettings
	Logger                observability.LogProvider
	DiagnosticProtector   observability.DiagnosticProtector
	Search                recallcontract.SearchRepository
	Provider              embeddingcontract.EmbeddingProviderInterface

	TopicMaintenanceConfig recall.TopicMaintenanceConfigProvider
	Metrics                observability.DiscoverabilityMetrics
}

func buildRecallApplication(deps recallApplicationDependencies) recall.RecallService {
	return recall.NewRecallService(recall.RecallDependencies{
		ExpansionEnabled: deps.ExpansionEnabled, DisableQueryEmbedding: deps.DisableQueryEmbedding,
		Decisions: deps.Decisions, DecisionSettings: deps.DecisionSettings, Logger: deps.Logger,
		DiagnosticProtector:    deps.DiagnosticProtector,
		Search:                 deps.Search,
		Provider:               deps.Provider,
		TopicMaintenanceConfig: deps.TopicMaintenanceConfig,
		Metrics:                deps.Metrics,
	})
}
