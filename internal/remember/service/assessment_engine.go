package service

import (
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// assessmentEngine contains request-scoped assessor mechanics only. It has no
// ledger, placement identity, lease, retry, or background-worker state.
type assessmentEngine struct {
	catalog        SubmissionAssessmentCatalog
	provider       assessor.Provider
	limits         assessor.SemanticAssessmentLimits
	teamID         string
	ownerProfileID string
	now            func() time.Time
	metrics        observability.DiscoverabilityMetrics
	logger         observability.LogProvider
}

func newAssessmentEngine(deps SynchronousAssessmentDependencies, teamID, ownerID string) *assessmentEngine {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &assessmentEngine{
		catalog: deps.Catalog, provider: deps.Provider, limits: deps.Limits,
		teamID: strings.TrimSpace(teamID), ownerProfileID: strings.TrimSpace(ownerID),
		now: time.Now, metrics: metrics, logger: deps.Logger,
	}
}
