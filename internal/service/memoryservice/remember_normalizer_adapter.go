package memoryservice

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// rememberNormalizerResponseToSemanticAssessmentResponse is a compatibility
// adapter for the existing atomic placement commit. The model-facing payload
// has no policy fields; these deterministic defaults are applied only at the
// legacy commit boundary and are never sent back to the provider.
func rememberNormalizerResponseToSemanticAssessment(
	req verifier.SemanticAssessmentRequest,
	normalized verifier.RememberNormalizerResponse,
) (verifier.SemanticAssessmentResponse, error) {
	response := verifier.SemanticAssessmentResponse{
		RequestID:           normalized.RequestID,
		SecuritySignals:     make([]verifier.SemanticAssessmentSecuritySignal, 0, len(normalized.SecuritySignals)),
		EntityResults:       make([]verifier.SemanticAssessmentEntityResult, 0, len(normalized.EntityResults)),
		RelationshipResults: make([]verifier.SemanticAssessmentRelationshipResult, 0, len(normalized.RelationshipResults)),
		InputTokens:         normalized.InputTokens,
		OutputTokens:        normalized.OutputTokens,
		ProviderTurns:       normalized.ProviderTurns,
	}
	for _, signal := range normalized.SecuritySignals {
		response.SecuritySignals = append(response.SecuritySignals, verifier.SemanticAssessmentSecuritySignal(signal))
	}
	entityTargetCount := 0
	if req.SubmissionContract != nil {
		entityTargetCount = len(req.SubmissionContract.Entities)
	}
	entityTargets := make(map[string]verifier.SemanticAssessmentRequiredEntityRef, entityTargetCount)
	if req.SubmissionContract != nil {
		for _, target := range req.SubmissionContract.Entities {
			entityTargets[target.Ref] = target
		}
	}
	predicateRangesByEntityRef := rememberNormalizerPredicateRangesByEntityRef(normalized.RelationshipResults)
	reservedGroundings := rememberNormalizerInitialGroundingReservations(entityTargets, normalized.EntityResults)
	for _, entity := range normalized.EntityResults {
		result := verifier.SemanticAssessmentEntityResult{
			Ref:               entity.Ref,
			GroundingRef:      entity.GroundingRef,
			Action:            entity.Action,
			CandidateEntityID: entity.CandidateEntityID,
			Confidence:        1,
			Rationale:         "normalized structure",
		}
		if target, ok := entityTargets[entity.Ref]; ok {
			result.Kind = target.Kind
			result.Surface = target.Name
			logicalKey := rememberNormalizerLogicalEntityKey(target)
			if grounding, ok := rememberNormalizerEntityGroundingWithReservations(req, target, entity, predicateRangesByEntityRef[entity.Ref], reservedGroundings, logicalKey); ok {
				result.GroundingRef = stringPointer(grounding.GroundingRef)
				result.Surface = grounding.Surface
				result.EvidenceID = grounding.EvidenceID
				result.Start, result.End = grounding.Start, grounding.End
				spanKey := rememberNormalizerGroundingSpanKey(grounding)
				if owner, exists := reservedGroundings[spanKey]; !exists || owner == logicalKey {
					reservedGroundings[spanKey] = logicalKey
				}
			}
		}
		response.EntityResults = append(response.EntityResults, result)
	}
	relationshipTargets := make(map[string]verifier.SemanticAssessmentRequiredRelationshipRef)
	if req.SubmissionContract != nil {
		for _, target := range req.SubmissionContract.Relationships {
			relationshipTargets[target.ProposalID] = target
		}
	}
	for _, relationship := range normalized.RelationshipResults {
		target, ok := relationshipTargets[relationship.Ref]
		if req.SubmissionContract != nil && !ok {
			return verifier.SemanticAssessmentResponse{}, fmt.Errorf("normalizer relationship %q is outside the submitted contract", relationship.Ref)
		}
		if ok && (!normalizerOptionalStringEqual(relationship.ValidFrom, target.ValidFrom) || !normalizerOptionalStringEqual(relationship.ValidTo, target.ValidTo)) {
			return verifier.SemanticAssessmentResponse{}, fmt.Errorf("normalizer relationship %q does not preserve submitted temporal bounds", relationship.Ref)
		}
		validFrom, validTo := relationship.ValidFrom, relationship.ValidTo
		if ok {
			validFrom, validTo = target.ValidFrom, target.ValidTo
		}
		result := verifier.SemanticAssessmentRelationshipResult{
			Ref:              relationship.Ref,
			SubjectRef:       relationship.SubjectRef,
			PredicateRange:   normalizerRangeToAssessmentRange(relationship.PredicateRange),
			PredicateStatus:  relationship.PredicateStatus,
			PredicateKey:     relationship.PredicateKey,
			PredicateVersion: relationship.PredicateVersion,
			ObjectRef:        relationship.ObjectRef,
			ObjectValue:      relationship.ObjectValue,
			Polarity:         relationship.Polarity,
			Modality:         relationship.Modality,
			ValidFrom:        validFrom,
			ValidTo:          validTo,
			ScopeStatus:      relationship.ScopeStatus,
			ScopeKey:         relationship.ScopeKey,
			Confidence:       1,
			Rationale:        "normalized structure",
			EvidenceVerdict:  "entailed",
			TemporalVerdict:  "absent",
		}
		if relationship.ValueRange != nil {
			valueRange := normalizerRangeToAssessmentRange(*relationship.ValueRange)
			result.ValueRange = &valueRange
		}
		for _, support := range relationship.SupportRanges {
			converted := normalizerRangeToAssessmentRange(support)
			result.SupportRanges = append(result.SupportRanges, converted)
			result.Evidence = append(result.Evidence, verifier.SemanticAssessmentEvidenceSpan{EvidenceID: converted.EvidenceID, Start: converted.Start, End: converted.End})
		}
		if result.PredicateRange.EvidenceID != "" {
			for _, evidence := range req.Evidence {
				if evidence.EvidenceID == result.PredicateRange.EvidenceID {
					quote, err := verifier.SemanticEvidenceSpan(evidence.Content, result.PredicateRange.Start, result.PredicateRange.End)
					if err != nil {
						return verifier.SemanticAssessmentResponse{}, fmt.Errorf("normalizer predicate span: %w", err)
					}
					result.OriginalPredicate = strings.TrimSpace(quote)
					break
				}
			}
		}
		response.RelationshipResults = append(response.RelationshipResults, result)
	}
	return response, nil
}

func normalizerOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// rememberNormalizerPredicateRangesByEntityRef keeps endpoint context for
// grounding selection. Repeated surface text can have multiple valid
// groundings, and the relationship predicate is the only immutable context
// that distinguishes the intended mention without trusting model policy.
func rememberNormalizerPredicateRangesByEntityRef(
	relationships []verifier.RememberNormalizerRelationshipResult,
) map[string][]verifier.RememberNormalizerRange {
	ranges := make(map[string][]verifier.RememberNormalizerRange)
	for _, relationship := range relationships {
		if relationship.SubjectRef != "" {
			ranges[relationship.SubjectRef] = append(ranges[relationship.SubjectRef], relationship.PredicateRange)
		}
		if relationship.ObjectRef != nil && *relationship.ObjectRef != "" {
			ranges[*relationship.ObjectRef] = append(ranges[*relationship.ObjectRef], relationship.PredicateRange)
		}
	}
	return ranges
}

func rememberNormalizerEntityGrounding(
	req verifier.RememberNormalizerRequest,
	target verifier.SemanticAssessmentRequiredEntityRef,
	entity verifier.RememberNormalizerEntityResult,
	predicateRanges []verifier.RememberNormalizerRange,
) (verifier.SemanticAssessmentEntityGrounding, bool) {
	return rememberNormalizerEntityGroundingWithReservations(req, target, entity, predicateRanges, nil, "")
}

type rememberNormalizerGroundingSpan struct {
	evidenceID string
	start      int
	end        int
}

func rememberNormalizerGroundingSpanKey(grounding verifier.SemanticAssessmentEntityGrounding) rememberNormalizerGroundingSpan {
	return rememberNormalizerGroundingSpan{evidenceID: grounding.EvidenceID, start: grounding.Start, end: grounding.End}
}

func rememberNormalizerLogicalEntityKey(target verifier.SemanticAssessmentRequiredEntityRef) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(target.Name)), " ")) + "\x00" + strings.TrimSpace(target.Kind)
}

func rememberNormalizerInitialGroundingReservations(
	targets map[string]verifier.SemanticAssessmentRequiredEntityRef,
	entities []verifier.RememberNormalizerEntityResult,
) map[rememberNormalizerGroundingSpan]string {
	reserved := make(map[rememberNormalizerGroundingSpan]string, len(entities))
	for _, entity := range entities {
		if entity.GroundingRef == nil {
			continue
		}
		target, ok := targets[entity.Ref]
		if !ok {
			continue
		}
		grounding, ok := rememberNormalizerGroundingByRef(target.Groundings, entity.GroundingRef)
		if !ok {
			continue
		}
		spanKey := rememberNormalizerGroundingSpanKey(grounding)
		if _, exists := reserved[spanKey]; !exists {
			reserved[spanKey] = rememberNormalizerLogicalEntityKey(target)
		}
	}
	return reserved
}

func rememberNormalizerEntityGroundingWithReservations(
	req verifier.RememberNormalizerRequest,
	target verifier.SemanticAssessmentRequiredEntityRef,
	entity verifier.RememberNormalizerEntityResult,
	predicateRanges []verifier.RememberNormalizerRange,
	reserved map[rememberNormalizerGroundingSpan]string,
	logicalKey string,
) (verifier.SemanticAssessmentEntityGrounding, bool) {
	providerGrounding, providerOK := rememberNormalizerGroundingByRef(target.Groundings, entity.GroundingRef)
	if !providerOK {
		return verifier.SemanticAssessmentEntityGrounding{}, false
	}
	if len(predicateRanges) == 0 {
		return providerGrounding, true
	}

	best := providerGrounding
	bestDistance := 0
	bestFound := false
	for _, predicateRange := range predicateRanges {
		if providerGrounding.EvidenceID != predicateRange.EvidenceID {
			continue
		}
		distance := rememberNormalizerSpanDistance(providerGrounding.Start, providerGrounding.End, predicateRange.Start, predicateRange.End)
		if !bestFound || distance < bestDistance {
			bestDistance = distance
			bestFound = true
		}
	}
	if !bestFound {
		return providerGrounding, true
	}
	for _, grounding := range target.Groundings {
		if !rememberNormalizerGroundingCompatible(req, target, entity, grounding) {
			continue
		}
		for _, predicateRange := range predicateRanges {
			if grounding.EvidenceID != predicateRange.EvidenceID {
				continue
			}
			if owner, exists := reserved[rememberNormalizerGroundingSpanKey(grounding)]; exists && owner != logicalKey {
				continue
			}
			distance := rememberNormalizerSpanDistance(grounding.Start, grounding.End, predicateRange.Start, predicateRange.End)
			if distance < bestDistance {
				best = grounding
				bestDistance = distance
			}
		}
	}
	return best, true
}

func rememberNormalizerGroundingByRef(
	groundings []verifier.SemanticAssessmentEntityGrounding,
	groundingRef *string,
) (verifier.SemanticAssessmentEntityGrounding, bool) {
	if groundingRef == nil {
		return verifier.SemanticAssessmentEntityGrounding{}, false
	}
	for _, grounding := range groundings {
		if grounding.GroundingRef == *groundingRef {
			return grounding, true
		}
	}
	return verifier.SemanticAssessmentEntityGrounding{}, false
}

func rememberNormalizerGroundingCompatible(
	req verifier.RememberNormalizerRequest,
	target verifier.SemanticAssessmentRequiredEntityRef,
	entity verifier.RememberNormalizerEntityResult,
	grounding verifier.SemanticAssessmentEntityGrounding,
) bool {
	if entity.Action == string(domain.EntityResolutionAmbiguous) {
		return true
	}
	truncated := false
	matching := make([]verifier.SemanticAssessmentEntityCandidate, 0)
	for _, group := range req.EntityCandidateGroups {
		if group.GroundingRef != grounding.GroundingRef {
			continue
		}
		truncated = truncated || group.CandidateContextTruncated
		for _, candidate := range group.Candidates {
			if candidate.Kind == target.Kind {
				matching = append(matching, candidate)
			}
		}
	}
	switch entity.Action {
	case string(domain.EntityResolutionReuse):
		return entity.CandidateEntityID != nil && !truncated && len(matching) == 1 && matching[0].EntityID == *entity.CandidateEntityID
	case string(domain.EntityResolutionCreate):
		return entity.CandidateEntityID == nil && !truncated && len(matching) == 0
	default:
		return false
	}
}

func rememberNormalizerSpanDistance(leftStart, leftEnd, rightStart, rightEnd int) int {
	if leftEnd < rightStart {
		return rightStart - leftEnd
	}
	if rightEnd < leftStart {
		return leftStart - rightEnd
	}
	return 0
}

// rememberNormalizerFinalResponseLimits keeps the model-facing output budget
// independent from deterministic compatibility fields added after the model
// response has been accepted. The final response remains bounded by its
// measured serialized size and is revalidated with that same bound when read.
func rememberNormalizerFinalResponseLimits(
	limits verifier.SemanticAssessmentLimits,
	response verifier.SemanticAssessmentResponse,
) (verifier.SemanticAssessmentLimits, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return limits, fmt.Errorf("marshal normalized assessment response: %w", err)
	}
	outputTokens, err := verifier.CountTokens(string(raw), limits.Tokenizer)
	if err != nil {
		return limits, fmt.Errorf("count normalized assessment response tokens: %w", err)
	}
	if outputTokens > limits.MaxOutputTokens {
		limits.MaxOutputTokens = outputTokens
	}
	return limits, nil
}

func normalizerRangeToAssessmentRange(value verifier.RememberNormalizerRange) verifier.SemanticAssessmentGroundedRange {
	return verifier.SemanticAssessmentGroundedRange{
		EvidenceID: value.EvidenceID,
		StartRef:   value.StartRef,
		EndRef:     value.EndRef,
		Confidence: 1,
		Start:      value.Start,
		End:        value.End,
	}
}
