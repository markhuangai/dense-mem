package assessor

// SemanticAssessmentSystemPrompt is the fixed provider instruction for the
// closed Remember assessor request.
const SemanticAssessmentSystemPrompt = semanticAssessmentSystemPrompt

// SemanticAssessmentCorrectionInstruction is the fixed instruction used for
// complete replacement responses during repair.
const SemanticAssessmentCorrectionInstruction = semanticAssessmentCorrectionInstruction

// ValidateSemanticAssessmentResponseRaw validates the closed JSON shape before
// request-dependent semantic validation.
func ValidateSemanticAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	return validateSemanticAssessmentResponseRaw(raw)
}

// ValidateSemanticAssessmentEvidenceSecurityResults validates the assessor's
// nested, evidence-scoped security table. It is the single security-result
// policy entry point shared by all provider adapters.
func ValidateSemanticAssessmentEvidenceSecurityResults(
	results []SemanticAssessmentEvidenceSecurityResult,
	evidenceByID map[string]SemanticReviewEvidence,
) []SemanticValidationError {
	return validateSemanticAssessmentEvidenceSecurityResults(results, evidenceByID)
}

// ResolveSemanticAssessmentRange converts a provider boundary range into
// canonical rune offsets after checking the submitted evidence allowlist.
func ResolveSemanticAssessmentRange(
	evidenceByID map[string]SemanticReviewEvidence,
	value *SemanticAssessmentGroundedRange,
) error {
	return resolveSemanticAssessmentRange(evidenceByID, value)
}

// ValidateSemanticAssessmentSubmissionResponse validates the complete
// submitted Entity and Relationship contract against an assessor response.
func ValidateSemanticAssessmentSubmissionResponse(
	contract *SemanticAssessmentSubmissionContract,
	response SemanticAssessmentResponse,
) []SemanticValidationError {
	return validateSemanticAssessmentSubmissionResponse(contract, response)
}

// NormalizeSemanticAssessmentLimits applies the contract defaults to an
// externally supplied limit set.
func NormalizeSemanticAssessmentLimits(limits SemanticAssessmentLimits) SemanticAssessmentLimits {
	return normalizeSemanticAssessmentLimits(limits)
}

// ValidateSemanticAssessmentLimits rejects an assessor budget that cannot fit
// the minimum request after reserving room for bounded response repairs.
func ValidateSemanticAssessmentLimits(limits SemanticAssessmentLimits) error {
	return validateSemanticAssessmentLimits(limits)
}
