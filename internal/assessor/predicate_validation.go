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
			return []SemanticValidationError{semanticCodedErr(field+".predicate_key", "predicate_key and predicate_version are required for resolved", "predicate_resolution_required")}
		}
		if result.PredicateRegistration != nil {
			return []SemanticValidationError{semanticCodedErr(field+".predicate_registration", "must be null for resolved", "predicate_registration_must_be_null")}
		}
		option, ok := predicates[assessmentPredicateKey(*result.PredicateKey, *result.PredicateVersion)]
		if !ok {
			return []SemanticValidationError{semanticCodedErr(field+".predicate_key", "is outside predicate allowlist", "predicate_outside_allowlist")}
		}
		var errs []SemanticValidationError
		if subjectOK && !semanticKindAllowed(subject.Kind, option.AllowedSubjectKinds) {
			errs = append(errs, semanticCodedErr(field+".predicate_key", "does not accept the subject kind", "predicate_subject_kind_mismatch"))
		}
		if objectOK && !semanticKindAllowed(objectKind, option.AllowedObjectKinds) {
			errs = append(errs, semanticCodedErr(field+".predicate_key", "does not accept the object kind", "predicate_object_kind_mismatch"))
		}
		return errs
	case "registration_required":
		if result.PredicateKey != nil || result.PredicateVersion != nil {
			return []SemanticValidationError{semanticCodedErr(field+".predicate_key", "predicate_key and predicate_version must be null for registration_required", "predicate_resolution_must_be_null")}
		}
		if result.PredicateRegistration == nil {
			return []SemanticValidationError{semanticCodedErr(field+".predicate_registration", "is required for registration_required", "predicate_registration_required")}
		}
		registration := result.PredicateRegistration
		var errs []SemanticValidationError
		if !assessmentBoundedRequiredString(registration.PredicateKey, 128) {
			errs = append(errs, semanticCodedErr(field+".predicate_registration.predicate_key", "is required and must be bounded", "predicate_key_invalid"))
		}
		if !semanticOneOf(registration.RelationshipKind, domain.RelationshipKinds()...) {
			errs = append(errs, semanticCodedErr(field+".predicate_registration.relationship_kind", "is unsupported", "predicate_relationship_kind_invalid"))
		}
		if !semanticOneOf(registration.CurrentCardinality, domain.CurrentCardinalities()...) {
			errs = append(errs, semanticCodedErr(field+".predicate_registration.current_cardinality", "is unsupported", "predicate_cardinality_invalid"))
		}
		return errs
	default:
		return []SemanticValidationError{semanticCodedErr(field+".predicate_status", "is unsupported", "predicate_status_invalid")}
	}
}
