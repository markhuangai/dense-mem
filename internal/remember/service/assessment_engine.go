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
	provider              assessor.Provider
	limits                assessor.SemanticAssessmentLimits
	teamID                string
	ownerProfileID        string
	now                   func() time.Time
	metrics               observability.DiscoverabilityMetrics
	logger                observability.LogProvider
	completedInputTokens  int
	completedOutputTokens int
	diagnostics           *assessmentDiagnostics
}

func newAssessmentEngine(deps SynchronousAssessmentDependencies, teamID, ownerID string) *assessmentEngine {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &assessmentEngine{
		provider: deps.Provider, limits: deps.Limits,
		teamID: strings.TrimSpace(teamID), ownerProfileID: strings.TrimSpace(ownerID),
		now: time.Now, metrics: metrics, logger: deps.Logger,
	}
}
