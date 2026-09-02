package assessor

import (
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func validateSemanticAssessmentPredicateResult(
	resultIndex int,
	splitIndex int,
	result SemanticAssessmentRelationshipSplit,
	predicates map[string]SemanticAssessmentPredicateOption,
	subject SemanticAssessmentEntityResult,
	subjectOK bool,
	objectKind string,
	objectOK bool,
) []SemanticValidationError {
	field := fmt.Sprintf("relationship_results[%d].splits[%d]", resultIndex, splitIndex)
	switch result.PredicateStatus {
	case "resolved":
		if result.PredicateKey == nil || *result.PredicateKey == "" || result.PredicateVersion == nil || *result.PredicateVersion < 1 {
			return []SemanticValidationError{semanticErr(field+".predicate_key", "predicate_key and predicate_version are required for resolved")}
		}
		if result.PredicateRegistration != nil {
			return []SemanticValidationError{semanticErr(field+".predicate_registration", "must be null for resolved")}
		}
		option, ok := predicates[assessmentPredicateKey(*result.PredicateKey, *result.PredicateVersion)]
		if !ok {
			return []SemanticValidationError{semanticErr(field+".predicate_key", "is outside predicate allowlist")}
		}
		var errs []SemanticValidationError
		if subjectOK && !semanticKindAllowed(subject.Kind, option.AllowedSubjectKinds) {
			errs = append(errs, semanticErr(field+".predicate_key", "does not accept the subject kind"))
		}
		if objectOK && !semanticKindAllowed(objectKind, option.AllowedObjectKinds) {
			errs = append(errs, semanticErr(field+".predicate_key", "does not accept the object kind"))
		}
		return errs
	case "registration_required":
		if result.PredicateKey != nil || result.PredicateVersion != nil {
			return []SemanticValidationError{semanticErr(field+".predicate_key", "predicate_key and predicate_version must be null for registration_required")}
		}
		if result.PredicateRegistration == nil {
			return []SemanticValidationError{semanticErr(field+".predicate_registration", "is required for registration_required")}
		}
		registration := result.PredicateRegistration
		var errs []SemanticValidationError
		if !assessmentBoundedRequiredString(registration.PredicateKey, 128) {
			errs = append(errs, semanticErr(field+".predicate_registration.predicate_key", "is required and must be bounded"))
		}
		if !semanticOneOf(registration.RelationshipKind, domain.RelationshipKinds()...) {
			errs = append(errs, semanticErr(field+".predicate_registration.relationship_kind", "is unsupported"))
		}
		if !semanticOneOf(registration.CurrentCardinality, domain.CurrentCardinalities()...) {
			errs = append(errs, semanticErr(field+".predicate_registration.current_cardinality", "is unsupported"))
		}
		return errs
	default:
		return []SemanticValidationError{semanticErr(field+".predicate_status", "is unsupported")}
	}
}
