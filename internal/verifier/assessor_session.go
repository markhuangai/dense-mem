package verifier

import "context"

// SemanticAssessmentSession is an opaque provider-owned conversation handle.
// The application passes it back to Repair without inspecting provider state.
type SemanticAssessmentSession interface {
	SessionID() string
}

// SemanticAssessmentTurn is one complete assessor response. ValidationErrors
// are repairable response-contract issues; provider and transport failures are
// returned as errors instead.
type SemanticAssessmentTurn struct {
	Response         SemanticAssessmentResponse
	ValidationErrors []SemanticValidationError
	ValidationStage  string
	InputTokens      int
	OutputTokens     int
	Turn             int
}

// SemanticAssessmentRepairRequest carries refreshed server-owned context and
// bounded validation feedback. The submitted semantic envelope remains fixed.
type SemanticAssessmentRepairRequest struct {
	Request          SemanticAssessmentRequest
	ValidationErrors []SemanticValidationError
}

// RememberAssessor is the application-owned Remember assessment session port.
// Assess and Repair each perform one provider turn; the application owns the
// semantic-turn bound and decides when refreshed candidates are required.
type RememberAssessor interface {
	Assess(context.Context, SemanticAssessmentRequest) (SemanticAssessmentSession, SemanticAssessmentTurn, error)
	Repair(context.Context, SemanticAssessmentSession, SemanticAssessmentRepairRequest) (SemanticAssessmentTurn, error)
	ModelName() string
}
