package memoryservice

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/verifier"
)

func submissionAssessmentObservationRef(relationshipRef string, splitIndex, splitCount int) string {
	if splitCount == 1 && splitIndex == 0 {
		return relationshipRef
	}
	value := fmt.Sprintf("%s#split:%d", relationshipRef, splitIndex)
	if len([]rune(value)) <= 128 {
		return value
	}
	hash := strings.TrimPrefix(semanticAssessmentHash([]byte(relationshipRef)), "sha256:")
	return fmt.Sprintf("split:%s:%d", hash, splitIndex)
}

func assessmentValidationStage(stage string) string {
	if strings.TrimSpace(stage) == "" {
		return "response_contract"
	}
	return stage
}

func semanticAssessmentValidationFieldFamiliesForService(errs []verifier.SemanticValidationError) []string {
	seen := make(map[string]struct{}, len(errs))
	result := make([]string, 0, len(errs))
	for _, err := range errs {
		field := strings.TrimSpace(err.Field)
		if field == "" {
			field = "other"
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	return result
}

func relationshipObjectKind(
	result verifier.SemanticAssessmentRelationshipSplit,
	entityKinds map[string]string,
	fallback string,
) string {
	if result.ObjectRef != nil {
		if kind := entityKinds[*result.ObjectRef]; kind != "" {
			return kind
		}
	}
	if result.ObjectValue != nil && strings.TrimSpace(result.ObjectValue.ValueType) != "" {
		return result.ObjectValue.ValueType
	}
	return fallback
}
