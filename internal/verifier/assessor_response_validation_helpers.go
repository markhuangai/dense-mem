package verifier

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ValidateSemanticAssessmentRequiredRelationshipRefs verifies that each
// server-required client proposal is represented by a result grounded in one
// of that proposal's submitted evidence items.
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
		if len(required.EvidenceIDs) == 0 {
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
		}
		for _, expectedEvidenceID := range required.EvidenceIDs {
			for _, actual := range result.SupportRanges {
				if actual.EvidenceID == expectedEvidenceID {
					matchesEvidence = true
					break
				}
			}
			if matchesEvidence {
				break
			}
		}
		if !matchesEvidence {
			errs = append(errs, semanticErr("relationship_results", "does not use trusted proposal evidence"))
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

func validateSemanticAssessmentEvidence(index int, spans []SemanticAssessmentEvidenceSpan, evidenceByID map[string]SemanticReviewEvidence) []SemanticValidationError {
	field := fmt.Sprintf("relationship_results[%d].evidence", index)
	if len(spans) == 0 || len(spans) > SemanticAssessmentMaxEvidenceSpans {
		return []SemanticValidationError{semanticErr(field, fmt.Sprintf("must contain between 1 and %d spans", SemanticAssessmentMaxEvidenceSpans))}
	}
	seen := map[string]struct{}{}
	var errs []SemanticValidationError
	for i, span := range spans {
		evidence, ok := evidenceByID[span.EvidenceID]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("%s[%d].evidence_id", field, i), "is unknown"))
			continue
		}
		if _, err := semanticExactSpanQuote(evidence.Content, span.Start, span.End, ""); err != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("%s[%d]", field, i), err.Error()))
		}
		key := assessmentSpanKey(span.EvidenceID, span.Start, span.End)
		if _, exists := seen[key]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("%s[%d]", field, i), "is duplicated"))
		}
		seen[key] = struct{}{}
	}
	return errs
}

func validateSemanticAssessmentTimeAndScope(index int, result SemanticAssessmentRelationshipResult) []SemanticValidationError {
	field := fmt.Sprintf("relationship_results[%d]", index)
	var errs []SemanticValidationError
	validFrom, fromErr := assessmentParsedTime(result.ValidFrom)
	if fromErr != nil {
		errs = append(errs, semanticErr(field+".valid_from", "must be an RFC3339 timestamp or null"))
	}
	validTo, toErr := assessmentParsedTime(result.ValidTo)
	if toErr != nil {
		errs = append(errs, semanticErr(field+".valid_to", "must be an RFC3339 timestamp or null"))
	}
	if fromErr == nil && toErr == nil && validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		errs = append(errs, semanticErr(field+".valid_to", "must not be before valid_from"))
	}
	if result.TemporalVerdict == "entailed" {
		if validFrom == nil && validTo == nil {
			errs = append(errs, semanticErr(field+".temporal_verdict", "entailed requires valid_from or valid_to"))
		}
	} else if result.ValidFrom != nil || result.ValidTo != nil {
		errs = append(errs, semanticErr(field+".temporal_verdict", "only entailed may provide validity bounds"))
	}
	switch result.ScopeStatus {
	case "resolved":
		if result.ScopeKey == nil || !assessmentBoundedRequiredString(*result.ScopeKey, 256) {
			errs = append(errs, semanticErr(field+".scope_key", "is required and must be bounded for resolved scope"))
		}
	case "absent", "needs_review":
		if result.ScopeKey != nil {
			errs = append(errs, semanticErr(field+".scope_key", "must be null unless scope_status is resolved"))
		}
	default:
		errs = append(errs, semanticErr(field+".scope_status", "is unsupported"))
	}
	return errs
}

func assessmentParsedTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func assessmentBoundedRequiredString(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len([]rune(value)) <= max
}

func assessmentCandidateGroupsBySpan(groups []SemanticAssessmentEntityCandidateGroup) map[string]SemanticAssessmentEntityCandidateGroup {
	out := make(map[string]SemanticAssessmentEntityCandidateGroup, len(groups))
	for _, group := range groups {
		out[assessmentSpanKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return out
}

func assessmentMatchingEntityCandidates(group SemanticAssessmentEntityCandidateGroup, kind string) []SemanticAssessmentEntityCandidate {
	matching := make([]SemanticAssessmentEntityCandidate, 0, len(group.Candidates))
	for _, candidate := range group.Candidates {
		if candidate.Kind == kind {
			matching = append(matching, candidate)
		}
	}
	return matching
}

func assessmentPredicateOptionsByKeyVersion(options []SemanticAssessmentPredicateOption) map[string]SemanticAssessmentPredicateOption {
	out := make(map[string]SemanticAssessmentPredicateOption, len(options))
	for _, option := range options {
		out[assessmentPredicateKey(option.PredicateKey, option.Version)] = option
	}
	return out
}

func assessmentSpanKey(evidenceID string, start int, end int) string {
	return fmt.Sprintf("%s:%d:%d", evidenceID, start, end)
}

func assessmentPredicateKey(key string, version int) string {
	return fmt.Sprintf("%s:%d", key, version)
}
