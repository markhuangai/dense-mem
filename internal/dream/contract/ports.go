package contract

import "context"

// DreamControlRepository contains read-only control-plane operations.
type DreamControlRepository interface {
	ListHypotheses(context.Context, ListHypothesesInput) ([]HypothesisRecord, string, error)
	GetHypothesis(context.Context, GetHypothesisInput) (*HypothesisRecord, error)
	CountHypotheses(context.Context, string, string) (int, error)
	ListDreamCyclesForTeam(context.Context, string, int) ([]DreamCycleRun, error)
}

// EvidenceDiscoveryRepository is the scheduler-only port for the hourly lane.
type EvidenceDiscoveryRepository interface {
	ListEvidenceDiscoveryTargets(context.Context, string, int, int) ([]EvidenceDiscoveryTargetInput, error)
	LoadEvidenceDiscoveryRunTotals(context.Context, string, string) (EvidenceDiscoveryRunTotals, error)
	PersistEvidenceDiscoveryEvaluation(context.Context, EvidenceDiscoveryEvaluationInput) (DreamGenerationPersistResult, error)
	WithEvidenceDiscoveryTargetLock(context.Context, string, string, string, func(EvidenceDiscoveryAttempt) error) error
	MarkEvidenceDiscoveryAttemptDispatched(context.Context, EvidenceDiscoveryAttemptValidationInput) error
	MarkEvidenceDiscoveryAttemptValidated(context.Context, EvidenceDiscoveryAttemptValidationInput) error
	AbandonEvidenceDiscoveryAttempt(context.Context, string, string, string) error
}

// EvidenceDiscoveryInputValidator is the admission check used immediately
// before dispatching a provider request.
type EvidenceDiscoveryInputValidator interface {
	ValidateEvidenceDiscoveryInputs(context.Context, string, EvidenceTarget, []EvidenceContext) error
}
