package memoryservice

// synchronousAssessmentEngine contains only the request-scoped assessor
// capabilities. It deliberately has no ledger, lease, retry, or placement
// state; those concerns are not part of the synchronous Remember boundary.
type synchronousAssessmentEngine struct {
	*assessmentEngine
}

func newSynchronousAssessmentEngine(deps SynchronousAssessmentDependencies, teamID, ownerID string) *synchronousAssessmentEngine {
	return &synchronousAssessmentEngine{assessmentEngine: newAssessmentEngine(deps, teamID, ownerID)}
}
