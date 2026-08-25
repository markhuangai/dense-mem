package verifier

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func validateSemanticAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	errs := assessor.ValidateSemanticAssessmentResponseRaw(raw)
	converted := make([]SemanticValidationError, 0, len(errs))
	for _, err := range errs {
		converted = append(converted, semanticErr(err.Field, err.Message))
	}
	return converted
}

func semanticAssessmentJoinedErrors(errs []SemanticValidationError) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}
