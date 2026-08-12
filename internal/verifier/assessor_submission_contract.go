package verifier

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// SemanticAssessmentRequiredRelationshipRef binds trusted server-side
// correction/conflict context to a client proposal without forwarding that
// context to the assessor provider.
type SemanticAssessmentRequiredRelationshipRef struct {
	ProposalID        string                           `json:"proposal_id"`
	Evidence          []SemanticAssessmentEvidenceSpan `json:"evidence"`
	SubjectRef        string                           `json:"-"`
	OriginalPredicate string                           `json:"-"`
	ObjectRef         *string                          `json:"-"`
	ObjectValue       *SemanticAssessmentValue         `json:"-"`
	Polarity          string                           `json:"-"`
	Modality          string                           `json:"-"`
}

// SemanticAssessmentSubmissionContract is the trusted server-side source for
// a closed, submission-wide assessment. Its JSON projection is regenerated
// during request preparation so provider-visible refs cannot drift from the
// validator's exact allowlist.
type SemanticAssessmentSubmissionContract struct {
	Entities      []SemanticAssessmentRequiredEntityRef
	Relationships []SemanticAssessmentRequiredRelationshipRef
}

type SemanticAssessmentRequiredEntityRef struct {
	Ref        string
	Surface    string
	Kind       string
	EvidenceID string
	Start      int
	End        int
}

type SemanticAssessmentSubmittedEntity struct {
	Ref        string `json:"ref"`
	Surface    string `json:"surface"`
	Kind       string `json:"kind"`
	EvidenceID string `json:"evidence_id"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type SemanticAssessmentSubmittedRelationship struct {
	Ref               string                           `json:"ref"`
	SubjectRef        string                           `json:"subject_ref"`
	OriginalPredicate string                           `json:"original_predicate"`
	ObjectRef         *string                          `json:"object_ref"`
	ObjectValue       *SemanticAssessmentValue         `json:"object_value"`
	Polarity          string                           `json:"polarity"`
	Modality          string                           `json:"modality"`
	Evidence          []SemanticAssessmentEvidenceSpan `json:"evidence"`
}

func normalizeSemanticAssessmentSubmissionContract(
	req *SemanticAssessmentRequest,
	evidenceByID map[string]SemanticReviewEvidence,
) []SemanticValidationError {
	if req.SubmissionContract == nil {
		if len(req.SubmittedEntities) > 0 || len(req.SubmittedRelationships) > 0 {
			return []SemanticValidationError{semanticErr("submission_contract", "is required when submitted contract fields are present")}
		}
		req.SubmittedEntities = nil
		req.SubmittedRelationships = nil
		return nil
	}

	contract := req.SubmissionContract
	if contract.Entities == nil {
		contract.Entities = []SemanticAssessmentRequiredEntityRef{}
	}
	if contract.Relationships == nil {
		contract.Relationships = []SemanticAssessmentRequiredRelationshipRef{}
	}
	var errs []SemanticValidationError
	if len(contract.Entities) == 0 || len(contract.Entities) > SemanticAssessmentMaxEntityResults {
		errs = append(errs, semanticErr("submission_contract.entities", fmt.Sprintf("must contain between 1 and %d entries", SemanticAssessmentMaxEntityResults)))
	}
	if len(contract.Relationships) == 0 || len(contract.Relationships) > SemanticAssessmentMaxRelationshipResults {
		errs = append(errs, semanticErr("submission_contract.relationships", fmt.Sprintf("must contain between 1 and %d entries", SemanticAssessmentMaxRelationshipResults)))
	}

	entityRefs := make(map[string]SemanticAssessmentRequiredEntityRef, len(contract.Entities))
	entitySpans := make(map[string]struct{}, len(contract.Entities))
	for i := range contract.Entities {
		target := &contract.Entities[i]
		target.Ref = strings.TrimSpace(target.Ref)
		target.Surface = strings.TrimSpace(target.Surface)
		target.Kind = strings.TrimSpace(target.Kind)
		target.EvidenceID = strings.TrimSpace(target.EvidenceID)
		field := fmt.Sprintf("submission_contract.entities[%d]", i)
		if target.Ref == "" || len([]rune(target.Ref)) > 128 {
			errs = append(errs, semanticErr(field+".ref", "is required and must be at most 128 characters"))
		}
		if _, exists := entityRefs[target.Ref]; exists {
			errs = append(errs, semanticErr(field+".ref", "is duplicated"))
		}
		evidence, ok := evidenceByID[target.EvidenceID]
		if !ok {
			errs = append(errs, semanticErr(field+".evidence_id", "is unknown"))
			continue
		}
		exact, err := semanticExactSpanQuote(evidence.Content, target.Start, target.End, target.Surface)
		if err != nil {
			errs = append(errs, semanticErr(field+".surface", err.Error()))
		} else {
			target.Surface = exact
		}
		if !semanticOneOf(target.Kind, domain.EntityKinds()...) {
			errs = append(errs, semanticErr(field+".kind", "is unsupported"))
		}
		spanKey := assessmentSpanKey(target.EvidenceID, target.Start, target.End)
		if _, exists := entitySpans[spanKey]; exists {
			errs = append(errs, semanticErr(field, "duplicates an entity evidence span"))
		}
		entitySpans[spanKey] = struct{}{}
		entityRefs[target.Ref] = *target
	}

	seenRelationships := make(map[string]struct{}, len(contract.Relationships))
	for i := range contract.Relationships {
		target := &contract.Relationships[i]
		target.ProposalID = strings.TrimSpace(target.ProposalID)
		target.SubjectRef = strings.TrimSpace(target.SubjectRef)
		target.OriginalPredicate = strings.TrimSpace(target.OriginalPredicate)
		target.Polarity = strings.TrimSpace(target.Polarity)
		target.Modality = strings.TrimSpace(target.Modality)
		field := fmt.Sprintf("submission_contract.relationships[%d]", i)
		if target.ProposalID == "" || len([]rune(target.ProposalID)) > 128 {
			errs = append(errs, semanticErr(field+".ref", "is required and must be at most 128 characters"))
		}
		if _, exists := seenRelationships[target.ProposalID]; exists {
			errs = append(errs, semanticErr(field+".ref", "is duplicated"))
		}
		seenRelationships[target.ProposalID] = struct{}{}
		if _, ok := entityRefs[target.SubjectRef]; !ok {
			errs = append(errs, semanticErr(field+".subject_ref", "must reference a submitted entity"))
		}
		if !assessmentBoundedRequiredString(target.OriginalPredicate, 256) {
			errs = append(errs, semanticErr(field+".original_predicate", "is required and must be bounded"))
		}
		if !semanticOneOf(target.Polarity, "+", "-") {
			errs = append(errs, semanticErr(field+".polarity", "is unsupported"))
		}
		if !semanticOneOf(target.Modality, "statement", "question", "proposal", "speculation", "quoted") {
			errs = append(errs, semanticErr(field+".modality", "is unsupported"))
		}
		if target.ObjectRef != nil {
			value := strings.TrimSpace(*target.ObjectRef)
			target.ObjectRef = &value
			if _, ok := entityRefs[value]; !ok {
				errs = append(errs, semanticErr(field+".object_ref", "must reference a submitted entity"))
			}
		}
		if (target.ObjectRef == nil) == (target.ObjectValue == nil) {
			errs = append(errs, semanticErr(field+".object", "requires exactly one object_ref or object_value"))
		}
		if target.ObjectValue != nil {
			errs = append(errs, normalizeSemanticAssessmentSubmissionValue(target.ObjectValue, field+".object_value")...)
		}
		if len(target.Evidence) == 0 || len(target.Evidence) > SemanticAssessmentMaxEvidenceSpans {
			errs = append(errs, semanticErr(field+".evidence", fmt.Sprintf("must contain between 1 and %d spans", SemanticAssessmentMaxEvidenceSpans)))
		} else {
			seenSpans := map[string]struct{}{}
			for j := range target.Evidence {
				span := &target.Evidence[j]
				span.EvidenceID = strings.TrimSpace(span.EvidenceID)
				evidence, ok := evidenceByID[span.EvidenceID]
				if !ok {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d].evidence_id", field, j), "is unknown"))
					continue
				}
				if _, err := semanticExactSpanQuote(evidence.Content, span.Start, span.End, ""); err != nil {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d]", field, j), "span is invalid"))
				}
				key := assessmentSpanKey(span.EvidenceID, span.Start, span.End)
				if _, exists := seenSpans[key]; exists {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.evidence[%d]", field, j), "is duplicated"))
				}
				seenSpans[key] = struct{}{}
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}

	req.SubmittedEntities = make([]SemanticAssessmentSubmittedEntity, 0, len(contract.Entities))
	for _, target := range contract.Entities {
		req.SubmittedEntities = append(req.SubmittedEntities, SemanticAssessmentSubmittedEntity(target))
	}
	req.SubmittedRelationships = make([]SemanticAssessmentSubmittedRelationship, 0, len(contract.Relationships))
	for _, target := range contract.Relationships {
		req.SubmittedRelationships = append(req.SubmittedRelationships, SemanticAssessmentSubmittedRelationship{
			Ref: target.ProposalID, SubjectRef: target.SubjectRef, OriginalPredicate: target.OriginalPredicate,
			ObjectRef: target.ObjectRef, ObjectValue: cloneSemanticAssessmentValue(target.ObjectValue),
			Polarity: target.Polarity, Modality: target.Modality,
			Evidence: append([]SemanticAssessmentEvidenceSpan(nil), target.Evidence...),
		})
	}
	return nil
}

func normalizeSemanticAssessmentSubmissionValue(value *SemanticAssessmentValue, field string) []SemanticValidationError {
	if value == nil {
		return []SemanticValidationError{semanticErr(field, "is required")}
	}
	value.ValueType = strings.TrimSpace(value.ValueType)
	value.CanonicalValue = strings.TrimSpace(value.CanonicalValue)
	if value.Display != nil {
		trimmed := strings.TrimSpace(*value.Display)
		value.Display = &trimmed
	}
	if value.Unit != nil {
		trimmed := strings.TrimSpace(*value.Unit)
		value.Unit = &trimmed
	}
	_, detail := assessmentRelationshipObjectKind(SemanticAssessmentRelationshipResult{ObjectValue: value}, map[string]SemanticAssessmentEntityResult{})
	if detail == "" {
		return nil
	}
	return []SemanticValidationError{semanticErr(field, detail)}
}

func validateSemanticAssessmentSubmissionResponse(
	contract *SemanticAssessmentSubmissionContract,
	response SemanticAssessmentResponse,
) []SemanticValidationError {
	if contract == nil {
		return nil
	}
	entityTargets := make(map[string]SemanticAssessmentRequiredEntityRef, len(contract.Entities))
	for _, target := range contract.Entities {
		entityTargets[target.Ref] = target
	}
	entities := make(map[string]SemanticAssessmentEntityResult, len(response.EntityResults))
	var errs []SemanticValidationError
	for i, result := range response.EntityResults {
		target, ok := entityTargets[result.Ref]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].ref", i), "is outside the submitted entity contract"))
			continue
		}
		entities[result.Ref] = result
		if result.Surface != target.Surface || result.Kind != target.Kind || result.EvidenceID != target.EvidenceID || result.Start != target.Start || result.End != target.End {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d]", i), "does not preserve its submitted entity target"))
		}
	}
	for ref := range entityTargets {
		if _, ok := entities[ref]; !ok {
			errs = append(errs, semanticErr("entity_results", "is missing a submitted entity target"))
		}
	}

	relationshipTargets := make(map[string]SemanticAssessmentRequiredRelationshipRef, len(contract.Relationships))
	for _, target := range contract.Relationships {
		relationshipTargets[target.ProposalID] = target
	}
	seen := make(map[string]struct{}, len(response.RelationshipResults))
	for i, result := range response.RelationshipResults {
		target, ok := relationshipTargets[result.Ref]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].ref", i), "is outside the submitted relationship contract"))
			continue
		}
		seen[result.Ref] = struct{}{}
		if result.SubjectRef != target.SubjectRef || result.OriginalPredicate != target.OriginalPredicate || result.Polarity != target.Polarity || result.Modality != target.Modality {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d]", i), "does not preserve its submitted relationship target"))
		}
		if !semanticAssessmentSubmittedObjectMatches(target, result) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].object", i), "does not preserve its submitted object"))
		}
		allowedSpans := make(map[string]struct{}, len(target.Evidence))
		for _, span := range target.Evidence {
			allowedSpans[assessmentSpanKey(span.EvidenceID, span.Start, span.End)] = struct{}{}
		}
		for _, span := range result.Evidence {
			if _, ok := allowedSpans[assessmentSpanKey(span.EvidenceID, span.Start, span.End)]; !ok {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].evidence", i), "contains a span outside the submitted relationship target"))
				break
			}
		}
	}
	for ref := range relationshipTargets {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, semanticErr("relationship_results", "is missing a submitted relationship target"))
		}
	}
	return errs
}

func semanticAssessmentSubmittedObjectMatches(
	target SemanticAssessmentRequiredRelationshipRef,
	result SemanticAssessmentRelationshipResult,
) bool {
	if target.ObjectRef != nil {
		return result.ObjectValue == nil && result.ObjectRef != nil && *result.ObjectRef == *target.ObjectRef
	}
	return result.ObjectRef == nil && semanticAssessmentValuesEqual(target.ObjectValue, result.ObjectValue)
}

func semanticAssessmentValuesEqual(left, right *SemanticAssessmentValue) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ValueType == right.ValueType &&
		left.CanonicalValue == right.CanonicalValue &&
		semanticAssessmentStringPointersEqual(left.Display, right.Display) &&
		semanticAssessmentStringPointersEqual(left.Unit, right.Unit)
}

func semanticAssessmentStringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func cloneSemanticAssessmentValue(value *SemanticAssessmentValue) *SemanticAssessmentValue {
	if value == nil {
		return nil
	}
	cloned := *value
	if value.Display != nil {
		display := *value.Display
		cloned.Display = &display
	}
	if value.Unit != nil {
		unit := *value.Unit
		cloned.Unit = &unit
	}
	return &cloned
}
