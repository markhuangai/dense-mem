package verifier

import "github.com/markhuangai/dense-mem/internal/assessor"

// ValidateSemanticAssessmentRequiredRelationshipRefs preserves the verifier
// package entry point while assessor owns the validation policy.
func ValidateSemanticAssessmentRequiredRelationshipRefs(
	requiredRefs []SemanticAssessmentRequiredRelationshipRef,
	results []SemanticAssessmentRelationshipResult,
) []SemanticValidationError {
	return assessor.ValidateSemanticAssessmentRequiredRelationshipRefs(requiredRefs, results)
}

func resolveSemanticAssessmentRange(
	evidenceByID map[string]SemanticReviewEvidence,
	value *SemanticAssessmentGroundedRange,
) error {
	return assessor.ResolveSemanticAssessmentRange(evidenceByID, value)
}

func validateSemanticAssessmentSubmissionResponse(
	contract *SemanticAssessmentSubmissionContract,
	response SemanticAssessmentResponse,
) []SemanticValidationError {
	return assessor.ValidateSemanticAssessmentSubmissionResponse(contract, response)
}
