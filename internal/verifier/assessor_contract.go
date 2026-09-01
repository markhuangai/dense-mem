package verifier

import "github.com/markhuangai/dense-mem/internal/assessor"

// SemanticAssessmentResponseSchema keeps the legacy verifier API pointed at
// the assessor-owned closed response contract.
func SemanticAssessmentResponseSchema() map[string]any {
	return assessor.SemanticAssessmentResponseSchema()
}
