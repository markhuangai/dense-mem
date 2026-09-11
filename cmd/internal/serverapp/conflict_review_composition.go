package serverapp

import (
	"errors"
	"time"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/conflict/assessment"
	conflictreview "github.com/markhuangai/dense-mem/internal/conflict/review"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type conflictReviewApplicationDependencies struct {
	Ledger interface {
		conflictreview.RunLedger
		ConflictStore() *conflictpostgres.Store
	}
	Provider         conflictreview.Provider
	Embeddings       conflictreview.EmbeddingProvider
	EmbeddingTimeout time.Duration
	Timezone         string
	Limits           conflictassessment.SemanticAssessmentLimits
	Metrics          observability.DiscoverabilityMetrics
}

func buildConflictReviewApplication(deps conflictReviewApplicationDependencies) (*conflictreview.Runner, error) {
	if deps.Ledger == nil {
		return nil, errors.New("conflict review composition: conflict store is required")
	}
	return conflictreview.NewRunner(
		deps.Ledger.ConflictStore(),
		deps.Provider,
		deps.Embeddings,
		deps.EmbeddingTimeout,
		deps.Timezone,
		deps.Limits,
		deps.Metrics,
	)
}
