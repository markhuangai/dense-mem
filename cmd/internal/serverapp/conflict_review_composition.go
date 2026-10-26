package serverapp

import (
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/conflict/assessment"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	conflictreview "github.com/markhuangai/dense-mem/internal/conflict/review"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type conflictReviewApplicationDependencies struct {
	Decisions        modelprovider.DecisionProvider
	DecisionSettings modelprovider.DecisionSettings
	Logger           observability.LogProvider
	Store            *conflictpostgres.Store
	Provider         conflictreview.Provider
	Embeddings       conflictreview.EmbeddingProvider
	EmbeddingTimeout time.Duration
	Timezone         string
	Limits           conflictassessment.SemanticAssessmentLimits
	Metrics          observability.DiscoverabilityMetrics
}

func buildConflictReviewApplication(deps conflictReviewApplicationDependencies) (*conflictreview.Runner, error) {
	if deps.Store == nil {
		return nil, errors.New("conflict review composition: conflict store is required")
	}
	return conflictreview.NewRunner(
		deps.Store,
		deps.Provider,
		deps.Embeddings,
		deps.EmbeddingTimeout,
		deps.Timezone,
		deps.Limits,
		deps.Metrics,
		conflictreview.DecisionOptions{Provider: deps.Decisions, Settings: deps.DecisionSettings, Logger: deps.Logger},
	)
}
