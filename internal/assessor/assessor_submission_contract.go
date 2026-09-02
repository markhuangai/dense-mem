package assessor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func normalizeOptionalAssessmentTime(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	parsed, err := assessmentParsedTime(&trimmed)
	if err != nil {
		return &trimmed
	}
	canonical := parsed.UTC().Format(time.RFC3339Nano)
	return &canonical
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

const SemanticAssessmentMaxEntityGroundings = 20

// SemanticAssessmentRequiredRelationshipRef binds trusted server-side context
// to one caller proposal while exposing only bounded assessment inputs.
type SemanticAssessmentRequiredRelationshipRef struct {
	ProposalID               string
	PredicateHint            string
	KnownPredicateKey        string
	EvidenceIDs              []string
	KnownEvidenceIDs         []string
	KnownEvidenceUnavailable bool `json:"-"`
	Evidence                 []SemanticAssessmentEvidenceSpan
	SubjectRef               string
	OriginalPredicate        string
	ObjectRef                *string
	ObjectValue              *SemanticAssessmentValue
	Polarity                 string
	ValidFrom                *string
	ValidTo                  *string
	MaxSplits                int
}

type SemanticAssessmentSubmissionContract struct {
	Entities      []SemanticAssessmentRequiredEntityRef
	Relationships []SemanticAssessmentRequiredRelationshipRef
}

type SemanticAssessmentRequiredEntityRef struct {
	Ref              string
	Name             string
	Kind             string
	KnownEntityID    string
	EvidenceIDs      []string
	KnownEvidenceIDs []string
	Groundings       []SemanticAssessmentEntityGrounding
	Anchors          []SemanticAssessmentEntityAnchor
	Surface          string
	EvidenceID       string
	Start            int
	End              int
}

type SemanticAssessmentEntityGrounding struct {
	GroundingRef string `json:"grounding_ref"`
	EvidenceID   string `json:"evidence_id"`
	Surface      string `json:"surface"`
	StartRef     string `json:"start_ref"`
	EndRef       string `json:"end_ref"`
	AnchorRef    string `json:"anchor_ref,omitempty"`
	Start        int    `json:"-"`
	End          int    `json:"-"`
}

// SemanticAssessmentEntityAnchor is a server-issued canonical/alias span
// that may be used to justify an anchored coreference grounding.
type SemanticAssessmentEntityAnchor struct {
	AnchorRef          string   `json:"anchor_ref"`
	EvidenceID         string   `json:"evidence_id"`
	Surface            string   `json:"surface"`
	StartRef           string   `json:"start_ref"`
	EndRef             string   `json:"end_ref"`
	CandidateEntityIDs []string `json:"candidate_entity_ids"`
	Start              int      `json:"-"`
	End                int      `json:"-"`
}

type SemanticAssessmentSubmittedEntity struct {
	Ref           string                              `json:"ref"`
	Name          string                              `json:"name"`
	Kind          string                              `json:"kind"`
	KnownEntityID string                              `json:"known_entity_id,omitempty"`
	Groundings    []SemanticAssessmentEntityGrounding `json:"groundings"`
	Anchors       []SemanticAssessmentEntityAnchor    `json:"anchors"`
}

type SemanticAssessmentSubmittedRelationship struct {
	Ref               string                   `json:"ref"`
	SubjectRef        string                   `json:"subject_ref"`
	PredicateHint     string                   `json:"predicate_hint"`
	KnownPredicateKey string                   `json:"known_predicate_key,omitempty"`
	ObjectRef         *string                  `json:"object_ref"`
	ObjectValue       *SemanticAssessmentValue `json:"object_value"`
	Polarity          string                   `json:"polarity"`
	EvidenceIDs       []string                 `json:"evidence_ids"`
	KnownEvidenceIDs  []string                 `json:"known_evidence_ids,omitempty"`
	ValidFrom         *string                  `json:"valid_from,omitempty"`
	ValidTo           *string                  `json:"valid_to,omitempty"`
}

func normalizeSemanticAssessmentSubmissionContract(
	req *SemanticAssessmentRequest,
	evidenceByID map[string]SemanticReviewEvidence,
	knownEvidenceByID map[string]SemanticReviewEvidence,
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
	allEvidenceByID := make(map[string]SemanticReviewEvidence, len(evidenceByID)+len(knownEvidenceByID))
	for evidenceID, evidence := range evidenceByID {
		allEvidenceByID[evidenceID] = evidence
	}
	for evidenceID, evidence := range knownEvidenceByID {
		allEvidenceByID[evidenceID] = evidence
	}
	if len(contract.Entities) > SemanticAssessmentMaxEntityResults {
		errs = append(errs, semanticErr("submission_contract.entities", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxEntityResults)))
	}
	if len(contract.Relationships) > SemanticAssessmentMaxRelationshipResults {
		errs = append(errs, semanticErr("submission_contract.relationships", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxRelationshipResults)))
	}
	if (len(contract.Entities) == 0) != (len(contract.Relationships) == 0) {
		errs = append(errs, semanticErr("submission_contract", "entities and relationships must both be empty or both be non-empty"))
	}

	entityRefs := make(map[string]SemanticAssessmentRequiredEntityRef, len(contract.Entities))
	groundingRefs := make(map[string]struct{})
	for i := range contract.Entities {
		target := &contract.Entities[i]
		target.Ref = strings.TrimSpace(target.Ref)
		target.Name = strings.TrimSpace(target.Name)
		target.Kind = strings.TrimSpace(target.Kind)
		target.KnownEntityID = strings.TrimSpace(target.KnownEntityID)
		target.EvidenceIDs = normalizedUniqueStrings(target.EvidenceIDs)
		target.KnownEvidenceIDs = normalizedUniqueStrings(target.KnownEvidenceIDs)
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
		for _, evidenceID := range target.KnownEvidenceIDs {
			if _, ok := knownEvidenceByID[evidenceID]; !ok {
				errs = append(errs, semanticErr(field+".known_evidence_ids", "contains unknown known evidence"))
			}
		}
		if len(target.Anchors) > SemanticAssessmentMaxEntityGroundings {
			errs = append(errs, semanticErr(field+".anchors", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxEntityGroundings)))
		}
		anchorRefs := make(map[string]struct{}, len(target.Anchors))
		for j := range target.Anchors {
			anchor := &target.Anchors[j]
			anchor.AnchorRef = strings.TrimSpace(anchor.AnchorRef)
			anchor.EvidenceID = strings.TrimSpace(anchor.EvidenceID)
			anchor.StartRef = strings.TrimSpace(anchor.StartRef)
			anchor.EndRef = strings.TrimSpace(anchor.EndRef)
			anchorField := fmt.Sprintf("%s.anchors[%d]", field, j)
			if !assessmentBoundedRequiredString(anchor.AnchorRef, 128) {
				errs = append(errs, semanticErr(anchorField+".anchor_ref", "is required and must be bounded"))
			}
			if _, exists := anchorRefs[anchor.AnchorRef]; exists {
				errs = append(errs, semanticErr(anchorField+".anchor_ref", "is duplicated"))
			}
			anchorRefs[anchor.AnchorRef] = struct{}{}
			if !submissionAssessmentAnchorSurfaceAllowed(anchor.Surface) {
				errs = append(errs, semanticErr(anchorField+".surface", "must be a canonical or alias Entity mention"))
			}
			if len(anchor.CandidateEntityIDs) == 0 || len(anchor.CandidateEntityIDs) > SemanticAssessmentMaxEntityCandidatesPerSurface {
				errs = append(errs, semanticErr(anchorField+".candidate_entity_ids", "must contain between 1 and the configured candidate bound"))
			}
			seenCandidateIDs := make(map[string]struct{}, len(anchor.CandidateEntityIDs))
			for candidateIndex, candidateID := range anchor.CandidateEntityIDs {
				candidateID = strings.TrimSpace(candidateID)
				anchor.CandidateEntityIDs[candidateIndex] = candidateID
				if !assessmentBoundedRequiredString(candidateID, 128) {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.candidate_entity_ids[%d]", anchorField, candidateIndex), "is required and must be bounded"))
				}
				if _, exists := seenCandidateIDs[candidateID]; exists {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.candidate_entity_ids[%d]", anchorField, candidateIndex), "is duplicated"))
				}
				seenCandidateIDs[candidateID] = struct{}{}
			}
			if !containsString(allowedEvidence, anchor.EvidenceID) && !containsString(stringSet(target.KnownEvidenceIDs), anchor.EvidenceID) {
				errs = append(errs, semanticErr(anchorField+".evidence_id", "is outside the entity evidence allowlist"))
				continue
			}
			evidence, ok := allEvidenceByID[anchor.EvidenceID]
			if !ok {
				errs = append(errs, semanticErr(anchorField+".evidence_id", "is unknown"))
				continue
			}
			start, startOK := semanticAssessmentBoundaryOffset(evidence, anchor.StartRef)
			end, endOK := semanticAssessmentBoundaryOffset(evidence, anchor.EndRef)
			if !startOK || !endOK || start < 0 || end <= start {
				errs = append(errs, semanticErr(anchorField, "contains invalid boundary references"))
				continue
			}
			anchor.Start, anchor.End = start, end
			exact, err := semanticExactSpanQuote(evidence.Content, start, end, anchor.Surface)
			if err != nil {
				errs = append(errs, semanticErr(anchorField+".surface", err.Error()))
			} else {
				anchor.Surface = exact
			}
		}
		if len(target.Groundings) > SemanticAssessmentMaxEntityGroundings {
			errs = append(errs, semanticErr(field+".groundings", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxEntityGroundings)))
			continue
		}
		for j := range target.Groundings {
			grounding := &target.Groundings[j]
			grounding.GroundingRef = strings.TrimSpace(grounding.GroundingRef)
			grounding.EvidenceID = strings.TrimSpace(grounding.EvidenceID)
			grounding.StartRef = strings.TrimSpace(grounding.StartRef)
			grounding.EndRef = strings.TrimSpace(grounding.EndRef)
			grounding.AnchorRef = strings.TrimSpace(grounding.AnchorRef)
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
			if grounding.AnchorRef != "" {
				if _, ok := anchorRefs[grounding.AnchorRef]; !ok {
					errs = append(errs, semanticErr(groundingField+".anchor_ref", "is outside the submitted anchor allowlist"))
				}
			}
		}
		entityRefs[target.Ref] = *target
	}

	seenRelationships := make(map[string]struct{}, len(contract.Relationships))
	for i := range contract.Relationships {
		target := &contract.Relationships[i]
		target.ProposalID = strings.TrimSpace(target.ProposalID)
		target.PredicateHint = strings.TrimSpace(target.PredicateHint)
		target.KnownPredicateKey = strings.TrimSpace(target.KnownPredicateKey)
		target.SubjectRef = strings.TrimSpace(target.SubjectRef)
		target.Polarity = strings.TrimSpace(target.Polarity)
		target.ValidFrom = normalizeOptionalAssessmentTime(target.ValidFrom)
		target.ValidTo = normalizeOptionalAssessmentTime(target.ValidTo)
		target.EvidenceIDs = normalizedUniqueStrings(target.EvidenceIDs)
		target.KnownEvidenceIDs = normalizedUniqueStrings(target.KnownEvidenceIDs)
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
		from, fromErr := assessmentParsedTime(target.ValidFrom)
		to, toErr := assessmentParsedTime(target.ValidTo)
		if fromErr != nil || toErr != nil {
			errs = append(errs, semanticErr(field+".validity", "must contain RFC3339 timestamps or null"))
		} else if from != nil && to != nil && to.Before(*from) {
			errs = append(errs, semanticErr(field+".valid_to", "must not be before valid_from"))
		}
		if len(target.EvidenceIDs) == 0 || len(target.EvidenceIDs) > SemanticAssessmentMaxEvidenceSpans {
			errs = append(errs, semanticErr(field+".evidence_ids", "must be present and bounded"))
		}
		for _, evidenceID := range target.EvidenceIDs {
			if _, ok := evidenceByID[evidenceID]; !ok {
				errs = append(errs, semanticErr(field+".evidence_ids", "contains unknown evidence"))
			}
		}
		if len(target.KnownEvidenceIDs) > SemanticAssessmentMaxEvidenceSpans {
			errs = append(errs, semanticErr(field+".known_evidence_ids", "must be bounded"))
		}
		for _, evidenceID := range target.KnownEvidenceIDs {
			if _, ok := knownEvidenceByID[evidenceID]; !ok {
				errs = append(errs, semanticErr(field+".known_evidence_ids", "contains unknown known evidence"))
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}

	req.SubmittedEntities = make([]SemanticAssessmentSubmittedEntity, 0, len(contract.Entities))
	for _, target := range contract.Entities {
		req.SubmittedEntities = append(req.SubmittedEntities, SemanticAssessmentSubmittedEntity{
			Ref:           target.Ref,
			Name:          target.Name,
			Kind:          target.Kind,
			KnownEntityID: target.KnownEntityID,
			Groundings:    append([]SemanticAssessmentEntityGrounding(nil), target.Groundings...),
			Anchors:       append([]SemanticAssessmentEntityAnchor(nil), target.Anchors...),
		})
	}
	req.SubmittedRelationships = make([]SemanticAssessmentSubmittedRelationship, 0, len(contract.Relationships))
	for _, target := range contract.Relationships {
		req.SubmittedRelationships = append(req.SubmittedRelationships, SemanticAssessmentSubmittedRelationship{
			Ref:               target.ProposalID,
			SubjectRef:        target.SubjectRef,
			PredicateHint:     target.PredicateHint,
			KnownPredicateKey: target.KnownPredicateKey,
			ObjectRef:         target.ObjectRef,
			ObjectValue:       cloneSemanticAssessmentValue(target.ObjectValue),
			Polarity:          target.Polarity,
			EvidenceIDs:       append([]string(nil), target.EvidenceIDs...),
			KnownEvidenceIDs:  append([]string(nil), target.KnownEvidenceIDs...),
			ValidFrom:         cloneOptionalString(target.ValidFrom),
			ValidTo:           cloneOptionalString(target.ValidTo),
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
	evidenceByID := semanticAssessmentAllEvidence(req)
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
			if result.AnchorRef != nil && strings.TrimSpace(*result.AnchorRef) != "" {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "requires a grounding_ref"))
			}
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
			if grounding.AnchorRef != "" {
				if result.AnchorRef == nil || strings.TrimSpace(*result.AnchorRef) != grounding.AnchorRef {
					errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "must match the grounding anchor"))
				} else if anchor, ok := entityAnchorByRef(target.Anchors, grounding.AnchorRef); !ok {
					errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "is outside the submitted anchor allowlist"))
				} else if !semanticAssessmentAnchorPrecedes(anchor, grounding, evidenceByID) {
					errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "does not precede the submitted mention"))
				} else {
					if result.Action != string(domain.EntityResolutionReuse) {
						errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].action", i), "anchored coreference must reuse the anchored Entity"))
					}
					if result.CandidateEntityID == nil || !containsString(stringSet(anchor.CandidateEntityIDs), *result.CandidateEntityID) {
						errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "does not match the selected Entity"))
					}
				}
			} else if result.AnchorRef != nil && strings.TrimSpace(*result.AnchorRef) != "" {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "is not valid for this grounding"))
			}
			if result.CandidateEntityID != nil {
				if group, ok := groupsByGroundingRef[selected]; ok {
					for _, candidate := range group.Candidates {
						if candidate.EntityID == *result.CandidateEntityID {
							result.Kind = candidate.Kind
							break
						}
					}
				}
			}
			continue
		}
		group, ok := groupsByGroundingRef[selected]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "is outside the grounding allowlist"))
			continue
		}
		if result.AnchorRef != nil && strings.TrimSpace(*result.AnchorRef) != "" {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].anchor_ref", i), "is outside the submitted anchor allowlist"))
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
		for j := range result.Splits {
			split := &result.Splits[j]
			path := fmt.Sprintf("relationship_results[%d].splits[%d]", i, j)
			if err := resolveSemanticAssessmentRange(evidenceByID, &split.PredicateRange); err != nil {
				errs = append(errs, semanticErr(path+".predicate_range", err.Error()))
			} else {
				evidence := evidenceByID[split.PredicateRange.EvidenceID]
				split.OriginalPredicate, _ = SemanticEvidenceSpan(evidence.Content, split.PredicateRange.Start, split.PredicateRange.End)
			}
			if split.ValueRange != nil {
				if err := resolveSemanticAssessmentRange(evidenceByID, split.ValueRange); err != nil {
					errs = append(errs, semanticErr(path+".value_range", err.Error()))
				}
			}
			split.Evidence = make([]SemanticAssessmentEvidenceSpan, 0, len(split.SupportRanges))
			for k := range split.SupportRanges {
				rangeValue := &split.SupportRanges[k]
				if err := resolveSemanticAssessmentRange(evidenceByID, rangeValue); err != nil {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.support_ranges[%d]", path, k), err.Error()))
					continue
				}
				split.Evidence = append(split.Evidence, SemanticAssessmentEvidenceSpan{
					EvidenceID: rangeValue.EvidenceID,
					Start:      rangeValue.Start,
					End:        rangeValue.End,
				})
			}
		}
	}
	return errs
}

func entityAnchorByRef(values []SemanticAssessmentEntityAnchor, ref string) (SemanticAssessmentEntityAnchor, bool) {
	ref = strings.TrimSpace(ref)
	for _, value := range values {
		if value.AnchorRef == ref {
			return value, true
		}
	}
	return SemanticAssessmentEntityAnchor{}, false
}

func semanticAssessmentAnchorPrecedes(anchor SemanticAssessmentEntityAnchor, grounding SemanticAssessmentEntityGrounding, evidenceByID map[string]SemanticReviewEvidence) bool {
	anchorEvidence, ok := evidenceByID[anchor.EvidenceID]
	if !ok {
		return false
	}
	groundingEvidence, ok := evidenceByID[grounding.EvidenceID]
	if !ok {
		return false
	}
	if anchorEvidence.EvidenceIndex < groundingEvidence.EvidenceIndex {
		return true
	}
	return anchorEvidence.EvidenceIndex == groundingEvidence.EvidenceIndex && anchor.End <= grounding.Start
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
	_, detail := assessmentRelationshipObjectKind(SemanticAssessmentRelationshipSplit{ObjectValue: value}, map[string]SemanticAssessmentEntityResult{})
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
		if target.KnownEntityID != "" {
			if result.Action != string(domain.EntityResolutionReuse) {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].action", i), "must reuse the exact known_entity_id"))
			}
			if result.CandidateEntityID == nil || strings.TrimSpace(*result.CandidateEntityID) != target.KnownEntityID {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "must equal the exact known_entity_id"))
			}
		}
		if result.GroundingRef == nil {
			if result.Action != string(domain.EntityResolutionAmbiguous) &&
				!semanticAssessmentAllowsUngroundedExactReuse(contract, response.RelationshipResults, result) {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].grounding_ref", i), "is required unless action is ambiguous"))
			}
			if result.CandidateEntityID != nil && result.Action != string(domain.EntityResolutionReuse) {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "requires reuse when grounding_ref is null"))
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
		if result.Disposition == "not_supported" {
			continue
		}
		if target.KnownEvidenceUnavailable {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].disposition", i), "must be not_supported when requested known evidence is unavailable"))
			continue
		}
		if target.MaxSplits > 0 && len(result.Splits) > target.MaxSplits {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].splits", i), fmt.Sprintf("must contain at most %d entries for this relationship", target.MaxSplits)))
		}
		submittedEvidence := stringSet(target.EvidenceIDs)
		allowedEvidence := stringSet(append(append([]string(nil), target.EvidenceIDs...), target.KnownEvidenceIDs...))
		for j, split := range result.Splits {
			path := fmt.Sprintf("relationship_results[%d].splits[%d]", i, j)
			hasSubmittedSupport := false
			if target.KnownPredicateKey != "" {
				if split.PredicateStatus != "resolved" {
					errs = append(errs, semanticErr(path+".predicate_status", "must resolve the exact known_predicate_key"))
				}
				if split.PredicateKey == nil || strings.TrimSpace(*split.PredicateKey) != target.KnownPredicateKey {
					errs = append(errs, semanticErr(path+".predicate_key", "must equal the exact known_predicate_key"))
				}
			}
			if split.SubjectRef != target.SubjectRef || split.Polarity != target.Polarity {
				errs = append(errs, semanticErr(path, "does not preserve its submitted relationship target"))
			}
			if !semanticAssessmentStringPointersEqual(split.ValidFrom, target.ValidFrom) || !semanticAssessmentStringPointersEqual(split.ValidTo, target.ValidTo) {
				errs = append(errs, semanticErr(path+".validity", "does not preserve the submitted temporal bounds"))
			}
			if !semanticAssessmentSubmittedObjectMatches(target, split) {
				errs = append(errs, semanticErr(path+".object", "does not preserve its submitted object"))
			}
			for _, entry := range []struct {
				field      string
				rangeValue *SemanticAssessmentGroundedRange
			}{
				{field: "predicate_range", rangeValue: &split.PredicateRange},
				{field: "value_range", rangeValue: split.ValueRange},
			} {
				field, rangeValue := entry.field, entry.rangeValue
				if rangeValue != nil && !containsString(allowedEvidence, rangeValue.EvidenceID) {
					errs = append(errs, semanticErr(path+"."+field+".evidence_id", semanticAssessmentRelationshipEvidenceAllowlistMessage(target)))
				}
			}
			if (target.ObjectValue == nil) != (split.ValueRange == nil) {
				errs = append(errs, semanticErr(path+".value_range", "must be present exactly for a Value object"))
			}
			seenSupports := map[string]struct{}{}
			for k, support := range split.SupportRanges {
				if !containsString(allowedEvidence, support.EvidenceID) {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.support_ranges[%d].evidence_id", path, k), semanticAssessmentRelationshipEvidenceAllowlistMessage(target)))
				}
				if containsString(submittedEvidence, support.EvidenceID) {
					hasSubmittedSupport = true
				}
				key := assessmentSpanKey(support.EvidenceID, support.Start, support.End)
				if _, exists := seenSupports[key]; exists {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.support_ranges[%d]", path, k), "is duplicated"))
				}
				seenSupports[key] = struct{}{}
			}
			if !hasSubmittedSupport {
				errs = append(errs, semanticErr(path+".support_ranges", "must contain at least one submitted support range"))
			}
			if !semanticAssessmentRangeCovered(split.PredicateRange, split.SupportRanges) {
				errs = append(errs, semanticErr(path+".predicate_range", "must be covered by a support range"))
			}
			if split.ValueRange != nil && !semanticAssessmentRangeCovered(*split.ValueRange, split.SupportRanges) {
				errs = append(errs, semanticErr(path+".value_range", "must be covered by a support range"))
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

func semanticAssessmentRelationshipEvidenceAllowlistMessage(target SemanticAssessmentRequiredRelationshipRef) string {
	if len(target.KnownEvidenceIDs) == 0 {
		return "is outside the submitted evidence allowlist"
	}
	return "is outside the submitted or authorized known evidence allowlist"
}

func semanticAssessmentAllowsUngroundedExactReuse(
	contract *SemanticAssessmentSubmissionContract,
	relationshipResults []SemanticAssessmentRelationshipResult,
	entityResult SemanticAssessmentEntityResult,
) bool {
	if contract == nil || entityResult.GroundingRef != nil ||
		entityResult.Action != string(domain.EntityResolutionReuse) || entityResult.CandidateEntityID == nil {
		return false
	}
	knownEntityID := ""
	for _, target := range contract.Entities {
		if target.Ref == entityResult.Ref {
			knownEntityID = target.KnownEntityID
			break
		}
	}
	if knownEntityID == "" || strings.TrimSpace(*entityResult.CandidateEntityID) != knownEntityID {
		return false
	}
	dependent := false
	for _, target := range contract.Relationships {
		usesEntity := target.SubjectRef == entityResult.Ref ||
			(target.ObjectRef != nil && *target.ObjectRef == entityResult.Ref)
		if !usesEntity {
			continue
		}
		dependent = true
		matches := 0
		for _, result := range relationshipResults {
			if result.Ref == target.ProposalID {
				matches++
				if result.Disposition != "not_supported" {
					return false
				}
			}
		}
		if matches != 1 {
			return false
		}
	}
	return dependent
}

func semanticAssessmentRangeCovered(value SemanticAssessmentGroundedRange, supports []SemanticAssessmentGroundedRange) bool {
	for _, support := range supports {
		if value.EvidenceID == support.EvidenceID && support.Start <= value.Start && support.End >= value.End {
			return true
		}
	}
	return false
}

func submissionAssessmentAnchorSurfaceAllowed(value string) bool {
	switch strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ") {
	case "i", "me", "my", "mine", "myself", "you", "your", "yours", "yourself", "yourselves",
		"he", "him", "his", "himself", "she", "her", "hers", "herself", "it", "its", "itself",
		"we", "us", "our", "ours", "ourselves", "they", "them", "their", "theirs", "themselves",
		"this", "that", "these", "those":
		return false
	default:
		return strings.TrimSpace(value) != ""
	}
}

func semanticAssessmentSubmittedObjectMatches(
	target SemanticAssessmentRequiredRelationshipRef,
	result SemanticAssessmentRelationshipSplit,
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
