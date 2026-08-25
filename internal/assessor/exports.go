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

// NormalizeSemanticAssessmentLimits applies the contract defaults to an
// externally supplied limit set.
func NormalizeSemanticAssessmentLimits(limits SemanticAssessmentLimits) SemanticAssessmentLimits {
	return normalizeSemanticAssessmentLimits(limits)
}
