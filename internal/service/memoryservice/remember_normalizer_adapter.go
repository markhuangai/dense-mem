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
			if grounding, ok := rememberNormalizerEntityGrounding(req, target, entity, predicateRangesByEntityRef[entity.Ref]); ok {
				result.GroundingRef = stringPointer(grounding.GroundingRef)
				result.Surface = grounding.Surface
				result.EvidenceID = grounding.EvidenceID
				result.Start, result.End = grounding.Start, grounding.End
			}
		}
		response.EntityResults = append(response.EntityResults, result)
	}
	for _, relationship := range normalized.RelationshipResults {
		predicateStatus := relationship.PredicateStatus
		if predicateStatus == "registration_required" {
			// The structure-only contract names the absence of a supplied
			// predicate explicitly; the existing policy/commit contract uses
			// needs_review for the same non-promotable state.
			predicateStatus = "needs_review"
		}
		result := verifier.SemanticAssessmentRelationshipResult{
			Ref:              relationship.Ref,
			SubjectRef:       relationship.SubjectRef,
			PredicateRange:   normalizerRangeToAssessmentRange(relationship.PredicateRange),
			PredicateStatus:  predicateStatus,
			PredicateKey:     relationship.PredicateKey,
			PredicateVersion: relationship.PredicateVersion,
			ObjectRef:        relationship.ObjectRef,
			ObjectValue:      relationship.ObjectValue,
			Polarity:         relationship.Polarity,
			Modality:         relationship.Modality,
			ValidFrom:        relationship.ValidFrom,
			ValidTo:          relationship.ValidTo,
			ScopeStatus:      relationship.ScopeStatus,
			ScopeKey:         relationship.ScopeKey,
			Confidence:       1,
			Rationale:        "normalized structure",
			EvidenceVerdict:  "entailed",
			TemporalVerdict:  "absent",
		}
		if relationship.ValidFrom != nil || relationship.ValidTo != nil {
			result.TemporalVerdict = "entailed"
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
