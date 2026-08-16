package verifier

import (
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// SemanticAssessmentRequiredRelationshipRef binds trusted server-side context
// to one caller proposal while exposing only bounded assessment inputs.
type SemanticAssessmentRequiredRelationshipRef struct {
	ProposalID        string
	PredicateHint     string
	EvidenceIDs       []string
	Evidence          []SemanticAssessmentEvidenceSpan
	SubjectRef        string
	OriginalPredicate string
	ObjectRef         *string
	ObjectValue       *SemanticAssessmentValue
	Polarity          string
	Modality          string
}

type SemanticAssessmentSubmissionContract struct {
	Entities      []SemanticAssessmentRequiredEntityRef
	Relationships []SemanticAssessmentRequiredRelationshipRef
}

type SemanticAssessmentRequiredEntityRef struct {
	Ref         string
	Name        string
	Kind        string
	EvidenceIDs []string
	Groundings  []SemanticAssessmentEntityGrounding
	Surface     string
	EvidenceID  string
	Start       int
	End         int
}

type SemanticAssessmentEntityGrounding struct {
	GroundingRef string `json:"grounding_ref"`
	EvidenceID   string `json:"evidence_id"`
	Surface      string `json:"surface"`
	StartRef     string `json:"start_ref"`
	EndRef       string `json:"end_ref"`
	Start        int    `json:"-"`
	End          int    `json:"-"`
}

type SemanticAssessmentSubmittedEntity struct {
	Ref        string                              `json:"ref"`
	Name       string                              `json:"name"`
	Kind       string                              `json:"kind"`
	Groundings []SemanticAssessmentEntityGrounding `json:"groundings"`
}

type SemanticAssessmentSubmittedRelationship struct {
	Ref           string                   `json:"ref"`
	SubjectRef    string                   `json:"subject_ref"`
	PredicateHint string                   `json:"predicate_hint"`
	ObjectRef     *string                  `json:"object_ref"`
	ObjectValue   *SemanticAssessmentValue `json:"object_value"`
	Polarity      string                   `json:"polarity"`
	Modality      string                   `json:"modality"`
	EvidenceIDs   []string                 `json:"evidence_ids"`
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
	groundingRefs := make(map[string]struct{})
	for i := range contract.Entities {
		target := &contract.Entities[i]
		target.Ref = strings.TrimSpace(target.Ref)
		target.Name = strings.TrimSpace(target.Name)
		target.Kind = strings.TrimSpace(target.Kind)
		target.EvidenceIDs = normalizedUniqueStrings(target.EvidenceIDs)
		field := fmt.Sprintf("submission_contract.entities[%d]", i)
		if !assessmentBoundedRequiredString(target.Ref, 128) {
			errs = append(errs, semanticErr(field+".ref", "is required and must be bounded"))
		}
		if _, exists := entityRefs[target.Ref]; exists {
			errs = append(errs, semanticErr(field+".ref", "is duplicated"))
		}
		if !assessmentBoundedRequiredString(target.Name, 256) {
			errs = append(errs, semanticErr(field+".name", "is required and must be bounded"))
		}
		if !semanticOneOf(target.Kind, domain.EntityKinds()...) {
			errs = append(errs, semanticErr(field+".kind", "is unsupported"))
		}
		allowedEvidence := stringSet(target.EvidenceIDs)
		if len(allowedEvidence) == 0 {
			errs = append(errs, semanticErr(field+".evidence_ids", "must not be empty"))
		}
		for evidenceID := range allowedEvidence {
			if _, ok := evidenceByID[evidenceID]; !ok {
				errs = append(errs, semanticErr(field+".evidence_ids", "contains unknown evidence"))
			}
		}
		for j := range target.Groundings {
			grounding := &target.Groundings[j]
			grounding.GroundingRef = strings.TrimSpace(grounding.GroundingRef)
			grounding.EvidenceID = strings.TrimSpace(grounding.EvidenceID)
			grounding.StartRef = strings.TrimSpace(grounding.StartRef)
			grounding.EndRef = strings.TrimSpace(grounding.EndRef)
			groundingField := fmt.Sprintf("%s.groundings[%d]", field, j)
			if !assessmentBoundedRequiredString(grounding.GroundingRef, 128) {
				errs = append(errs, semanticErr(groundingField+".grounding_ref", "is required and must be bounded"))
			}
			if _, exists := groundingRefs[grounding.GroundingRef]; exists {
				errs = append(errs, semanticErr(groundingField+".grounding_ref", "is duplicated"))
			}
			groundingRefs[grounding.GroundingRef] = struct{}{}
			evidence, ok := evidenceByID[grounding.EvidenceID]
			if !ok || !containsString(allowedEvidence, grounding.EvidenceID) {
				errs = append(errs, semanticErr(groundingField+".evidence_id", "is outside the entity evidence allowlist"))
				continue
			}
			start, startOK := semanticAssessmentBoundaryOffset(evidence, grounding.StartRef)
			end, endOK := semanticAssessmentBoundaryOffset(evidence, grounding.EndRef)
			if !startOK || !endOK || start < 0 || end <= start {
				errs = append(errs, semanticErr(groundingField, "contains invalid boundary references"))
				continue
			}
			exact, err := semanticExactSpanQuote(evidence.Content, start, end, grounding.Surface)
			if err != nil {
				errs = append(errs, semanticErr(groundingField+".surface", err.Error()))
				continue
			}
			grounding.Surface = exact
			grounding.Start = start
			grounding.End = end
		}
		entityRefs[target.Ref] = *target
	}

	seenRelationships := make(map[string]struct{}, len(contract.Relationships))
	for i := range contract.Relationships {
		target := &contract.Relationships[i]
		target.ProposalID = strings.TrimSpace(target.ProposalID)
		target.PredicateHint = strings.TrimSpace(target.PredicateHint)
		target.SubjectRef = strings.TrimSpace(target.SubjectRef)
		target.Polarity = strings.TrimSpace(target.Polarity)
		target.Modality = strings.TrimSpace(target.Modality)
		target.EvidenceIDs = normalizedUniqueStrings(target.EvidenceIDs)
		field := fmt.Sprintf("submission_contract.relationships[%d]", i)
		if !assessmentBoundedRequiredString(target.ProposalID, 128) {
			errs = append(errs, semanticErr(field+".ref", "is required and must be bounded"))
		}
		if _, exists := seenRelationships[target.ProposalID]; exists {
			errs = append(errs, semanticErr(field+".ref", "is duplicated"))
		}
		seenRelationships[target.ProposalID] = struct{}{}
		if !assessmentBoundedRequiredString(target.PredicateHint, 128) {
			errs = append(errs, semanticErr(field+".predicate_hint", "is required and must be bounded"))
		}
		if _, ok := entityRefs[target.SubjectRef]; !ok {
			errs = append(errs, semanticErr(field+".subject_ref", "must reference a submitted entity"))
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
		if !semanticOneOf(target.Polarity, "+", "-") {
			errs = append(errs, semanticErr(field+".polarity", "is unsupported"))
		}
		if !semanticOneOf(target.Modality, "statement", "question", "proposal", "speculation", "quoted") {
			errs = append(errs, semanticErr(field+".modality", "is unsupported"))
		}
		if len(target.EvidenceIDs) == 0 || len(target.EvidenceIDs) > SemanticAssessmentMaxEvidenceSpans {
			errs = append(errs, semanticErr(field+".evidence_ids", "must be present and bounded"))
		}
		for _, evidenceID := range target.EvidenceIDs {
			if _, ok := evidenceByID[evidenceID]; !ok {
				errs = append(errs, semanticErr(field+".evidence_ids", "contains unknown evidence"))
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}

	req.SubmittedEntities = make([]SemanticAssessmentSubmittedEntity, 0, len(contract.Entities))
	for _, target := range contract.Entities {
		req.SubmittedEntities = append(req.SubmittedEntities, SemanticAssessmentSubmittedEntity{
			Ref:        target.Ref,
			Name:       target.Name,
			Kind:       target.Kind,
			Groundings: append([]SemanticAssessmentEntityGrounding(nil), target.Groundings...),
		})
	}
	req.SubmittedRelationships = make([]SemanticAssessmentSubmittedRelationship, 0, len(contract.Relationships))
	for _, target := range contract.Relationships {
		req.SubmittedRelationships = append(req.SubmittedRelationships, SemanticAssessmentSubmittedRelationship{
			Ref:           target.ProposalID,
			SubjectRef:    target.SubjectRef,
			PredicateHint: target.PredicateHint,
			ObjectRef:     target.ObjectRef,
			ObjectValue:   cloneSemanticAssessmentValue(target.ObjectValue),
			Polarity:      target.Polarity,
			Modality:      target.Modality,
			EvidenceIDs:   append([]string(nil), target.EvidenceIDs...),
		})
	}
	return nil
}

func resolveSemanticAssessmentSubmissionResponse(
	req SemanticAssessmentRequest,
	response *SemanticAssessmentResponse,
) []SemanticValidationError {
	if response == nil {
		return nil
	}
	evidenceByID := semanticEvidenceByID(req.Evidence)
	entityTargets := map[string]SemanticAssessmentRequiredEntityRef{}
	if req.SubmissionContract != nil {
		entityTargets = make(map[string]SemanticAssessmentRequiredEntityRef, len(req.SubmissionContract.Entities))
		for _, target := range req.SubmissionContract.Entities {
			entityTargets[target.Ref] = target
		}
	}
	groupsByGroundingRef := make(map[string]SemanticAssessmentEntityCandidateGroup, len(req.EntityCandidateGroups))
	for _, group := range req.EntityCandidateGroups {
		groupsByGroundingRef[group.GroundingRef] = group
	}
	var errs []SemanticValidationError
	for i := range response.EntityResults {
		result := &response.EntityResults[i]
		target, hasTarget := entityTargets[result.Ref]
		if hasTarget {
			result.Kind = target.Kind
			result.Surface = target.Name
		}
		if result.GroundingRef == nil {
			continue
		}
		selected := strings.TrimSpace(*result.GroundingRef)
		result.GroundingRef = &selected
		if hasTarget {
			grounding, ok := entityGroundingByRef(target.Groundings, selected)
			if !ok {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "is outside the submitted grounding allowlist"))
				continue
			}
			result.Surface = grounding.Surface
			result.EvidenceID = grounding.EvidenceID
			result.Start = grounding.Start
			result.End = grounding.End
			continue
		}
		group, ok := groupsByGroundingRef[selected]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "is outside the grounding allowlist"))
			continue
		}
		result.Surface = group.Surface
		result.EvidenceID = group.EvidenceID
		result.Start = group.Start
		result.End = group.End
		if result.CandidateEntityID != nil {
			for _, candidate := range group.Candidates {
				if candidate.EntityID == *result.CandidateEntityID {
					result.Kind = candidate.Kind
					break
				}
			}
		}
	}

	for i := range response.RelationshipResults {
		result := &response.RelationshipResults[i]
		if err := resolveSemanticAssessmentRange(evidenceByID, &result.PredicateRange); err != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_range", i), err.Error()))
		} else {
			evidence := evidenceByID[result.PredicateRange.EvidenceID]
			result.OriginalPredicate, _ = SemanticEvidenceSpan(evidence.Content, result.PredicateRange.Start, result.PredicateRange.End)
		}
		if result.ValueRange != nil {
			if err := resolveSemanticAssessmentRange(evidenceByID, result.ValueRange); err != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].value_range", i), err.Error()))
			}
		}
		result.Evidence = make([]SemanticAssessmentEvidenceSpan, 0, len(result.SupportRanges))
		for j := range result.SupportRanges {
			rangeValue := &result.SupportRanges[j]
			if err := resolveSemanticAssessmentRange(evidenceByID, rangeValue); err != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].support_ranges[%d]", i, j), err.Error()))
				continue
			}
			result.Evidence = append(result.Evidence, SemanticAssessmentEvidenceSpan{
				EvidenceID: rangeValue.EvidenceID,
				Start:      rangeValue.Start,
				End:        rangeValue.End,
			})
		}
	}
	return errs
}

func resolveSemanticAssessmentRange(
	evidenceByID map[string]SemanticReviewEvidence,
	value *SemanticAssessmentGroundedRange,
) error {
	if value == nil {
		return fmt.Errorf("range is required")
	}
	value.EvidenceID = strings.TrimSpace(value.EvidenceID)
	value.StartRef = strings.TrimSpace(value.StartRef)
	value.EndRef = strings.TrimSpace(value.EndRef)
	evidence, ok := evidenceByID[value.EvidenceID]
	if !ok {
		return fmt.Errorf("evidence_id is unknown")
	}
	start, startOK := semanticAssessmentBoundaryOffset(evidence, value.StartRef)
	end, endOK := semanticAssessmentBoundaryOffset(evidence, value.EndRef)
	if !startOK || !endOK || start < 0 || end <= start {
		return fmt.Errorf("boundary references are invalid")
	}
	value.Start = start
	value.End = end
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
		if result.GroundingRef == nil {
			if result.Action != string(domain.EntityResolutionAmbiguous) || result.CandidateEntityID != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "may be null only for an ambiguous result"))
			}
			continue
		}
		if _, ok := entityGroundingByRef(target.Groundings, *result.GroundingRef); !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "is outside the submitted entity contract"))
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
		if result.SubjectRef != target.SubjectRef || result.Polarity != target.Polarity || result.Modality != target.Modality {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d]", i), "does not preserve its submitted relationship target"))
		}
		if !semanticAssessmentSubmittedObjectMatches(target, result) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].object", i), "does not preserve its submitted object"))
		}
		allowedEvidence := stringSet(target.EvidenceIDs)
		for field, rangeValue := range map[string]*SemanticAssessmentGroundedRange{
			"predicate_range": &result.PredicateRange,
			"value_range":     result.ValueRange,
		} {
			if rangeValue != nil && !containsString(allowedEvidence, rangeValue.EvidenceID) {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].%s.evidence_id", i, field), "is outside the submitted evidence allowlist"))
			}
		}
		if (target.ObjectValue == nil) != (result.ValueRange == nil) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].value_range", i), "must be present exactly for a Value object"))
		}
		seenSupports := map[string]struct{}{}
		for j, support := range result.SupportRanges {
			if !containsString(allowedEvidence, support.EvidenceID) {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].support_ranges[%d].evidence_id", i, j), "is outside the submitted evidence allowlist"))
			}
			key := assessmentSpanKey(support.EvidenceID, support.Start, support.End)
			if _, exists := seenSupports[key]; exists {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].support_ranges[%d]", i, j), "is duplicated"))
			}
			seenSupports[key] = struct{}{}
		}
		if !semanticAssessmentRangeCovered(result.PredicateRange, result.SupportRanges) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_range", i), "must be covered by a support range"))
		}
		if result.ValueRange != nil && !semanticAssessmentRangeCovered(*result.ValueRange, result.SupportRanges) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].value_range", i), "must be covered by a support range"))
		}
	}
	for ref := range relationshipTargets {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, semanticErr("relationship_results", "is missing a submitted relationship target"))
		}
	}
	return errs
}

func semanticAssessmentRangeCovered(value SemanticAssessmentGroundedRange, supports []SemanticAssessmentGroundedRange) bool {
	for _, support := range supports {
		if value.EvidenceID == support.EvidenceID && support.Start <= value.Start && support.End >= value.End {
			return true
		}
	}
	return false
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
		return left == nil && right == nil
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

func entityGroundingByRef(values []SemanticAssessmentEntityGrounding, ref string) (SemanticAssessmentEntityGrounding, bool) {
	ref = strings.TrimSpace(ref)
	for _, value := range values {
		if value.GroundingRef == ref {
			return value, true
		}
	}
	return SemanticAssessmentEntityGrounding{}, false
}

func normalizedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func containsString(values map[string]struct{}, value string) bool {
	_, ok := values[value]
	return ok
}
