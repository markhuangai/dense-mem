package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/conflictreview"
)

type conflictReviewApplicationDependencies struct {
	Ledger           conflictreview.RunLedger
	Provider         conflictreview.Provider
	Embeddings       conflictreview.EmbeddingProvider
	EmbeddingTimeout time.Duration
	Timezone         string
	Limits           conflictassessment.SemanticAssessmentLimits
	Metrics          observability.DiscoverabilityMetrics
}

func buildConflictReviewApplication(deps conflictReviewApplicationDependencies) (*conflictreview.Runner, error) {
	return conflictreview.NewRunner(
		deps.Ledger,
		deps.Provider,
		deps.Embeddings,
		deps.EmbeddingTimeout,
		deps.Timezone,
		deps.Limits,
		deps.Metrics,
	)
}
