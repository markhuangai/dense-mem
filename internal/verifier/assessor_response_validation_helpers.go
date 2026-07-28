package verifier

import (
	"math"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ValidateSemanticAssessmentRequiredRelationshipRefs verifies that each
// server-required client proposal is represented by a result retaining one of
// that proposal's submitted evidence spans.
func ValidateSemanticAssessmentRequiredRelationshipRefs(
	requiredRefs []SemanticAssessmentRequiredRelationshipRef,
	results []SemanticAssessmentRelationshipResult,
) []SemanticValidationError {
	if len(requiredRefs) == 0 {
		return nil
	}
	byRef := make(map[string]SemanticAssessmentRelationshipResult, len(results))
	for _, result := range results {
		byRef[result.Ref] = result
	}
	var errs []SemanticValidationError
	for _, required := range requiredRefs {
		result, ok := byRef[required.ProposalID]
		if !ok {
			errs = append(errs, semanticErr("relationship_results", "missing result for trusted proposal"))
			continue
		}
		matchesEvidence := false
		for _, expected := range required.Evidence {
			for _, actual := range result.Evidence {
				if actual.EvidenceID == expected.EvidenceID && actual.Start == expected.Start && actual.End == expected.End {
					matchesEvidence = true
					break
				}
			}
			if matchesEvidence {
				break
			}
		}
		if !matchesEvidence {
			errs = append(errs, semanticErr("relationship_results", "does not retain a trusted proposal evidence span"))
		}
	}
	return errs
}

func assessmentConfidenceValid(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func assessmentRelationshipObjectKind(result SemanticAssessmentRelationshipResult, entities map[string]SemanticAssessmentEntityResult) (string, string) {
	hasRef := result.ObjectRef != nil && *result.ObjectRef != ""
	hasValue := result.ObjectValue != nil
	if hasRef == hasValue {
		return "", "requires exactly one object_ref or object_value"
	}
	if hasRef {
		entity, ok := entities[*result.ObjectRef]
		if !ok {
			return "", "object_ref is unknown"
		}
		return entity.Kind, ""
	}
	if !semanticOneOf(result.ObjectValue.ValueType, domain.ValueTypes()...) {
		return "", "object_value.value_type is unsupported"
	}
	if !assessmentBoundedRequiredString(result.ObjectValue.CanonicalValue, 4096) {
		return "", "object_value.canonical_value is required and must be bounded"
	}
	if result.ObjectValue.Display != nil && len([]rune(*result.ObjectValue.Display)) > 4096 {
		return "", "object_value.display must be bounded"
	}
	if result.ObjectValue.Unit != nil && len([]rune(*result.ObjectValue.Unit)) > 128 {
		return "", "object_value.unit must be bounded"
	}
	return result.ObjectValue.ValueType, ""
}
