package service

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
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

func semanticAssessmentValidationFieldFamiliesForService(errs []assessor.SemanticValidationError) []string {
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
	result assessor.SemanticAssessmentRelationshipSplit,
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

func submissionAssessmentPredicateRegistrations(
	plan submissionAssessmentPlan,
	response assessor.SemanticAssessmentResponse,
) ([]repository.SubmissionPredicateRegistrationInput, []string) {
	entityKinds := make(map[string]string, len(response.EntityResults))
	unsupportedEntities := repairSubmissionAssessmentResponse(&plan, &response)
	for _, result := range response.EntityResults {
		if _, unsupported := unsupportedEntities[result.Ref]; unsupported {
			continue
		}
		entityKinds[result.Ref] = result.Kind
	}
	registrations := make([]repository.SubmissionPredicateRegistrationInput, 0)
	paths := make([]string, 0)
	for resultIndex, result := range response.RelationshipResults {
		if result.Disposition != "stored" {
			continue
		}
		target := plan.relationshipsByRef[result.Ref]
		for splitIndex, split := range result.Splits {
			if split.PredicateStatus != "registration_required" || split.PredicateRegistration == nil {
				continue
			}
			registrations = append(registrations, repository.SubmissionPredicateRegistrationInput{
				RelationshipRef:    submissionAssessmentObservationRef(result.Ref, split.SplitIndex, len(result.Splits)),
				PredicateKey:       split.PredicateRegistration.PredicateKey,
				SubjectKind:        entityKinds[split.SubjectRef],
				ObjectKind:         relationshipObjectKind(split, entityKinds, target.ObjectKind),
				RelationshipKind:   split.PredicateRegistration.RelationshipKind,
				CurrentCardinality: split.PredicateRegistration.CurrentCardinality,
			})
			paths = append(paths, fmt.Sprintf("relationship_results[%d].splits[%d].predicate_registration", resultIndex, splitIndex))
		}
	}
	return registrations, paths
}
