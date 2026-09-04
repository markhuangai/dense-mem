package assessor

import (
	"fmt"
	"sort"
	"strings"
)

type semanticConflictCitableEvidence struct {
	evidence     SemanticReviewEvidence
	submitted    bool
	known        bool
	candidateFor []string
}

func semanticAssessmentConflictCitableEvidence(req SemanticAssessmentRequest) map[string]semanticConflictCitableEvidence {
	out := make(map[string]semanticConflictCitableEvidence, len(req.Evidence)+len(req.KnownEvidence))
	for _, item := range req.Evidence {
		out[item.EvidenceID] = semanticConflictCitableEvidence{evidence: item, submitted: true}
	}
	for _, item := range req.KnownEvidence {
		if _, exists := out[item.EvidenceID]; exists {
			continue
		}
		out[item.EvidenceID] = semanticConflictCitableEvidence{evidence: item, known: true}
	}
	for _, group := range req.EvidenceEquivalenceCandidates {
		for _, candidate := range group.Candidates {
			if _, exists := out[candidate.EvidenceID]; exists {
				continue
			}
			prepared := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
				EvidenceID:     candidate.EvidenceID,
				Content:        candidate.Content,
				BoundaryText:   candidate.BoundaryText,
				BoundaryRefs:   candidate.BoundaryRefs,
				BoundaryPrefix: candidate.BoundaryPrefix,
			})
			if len(candidate.BoundaryRefs) > 0 {
				prepared.BoundaryText = candidate.BoundaryText
				prepared.BoundaryRefs = candidate.BoundaryRefs
				prepared.BoundaryPrefix = candidate.BoundaryPrefix
			}
			out[candidate.EvidenceID] = semanticConflictCitableEvidence{evidence: prepared, candidateFor: []string{group.EvidenceID}}
		}
	}
	for _, group := range req.EvidenceEquivalenceCandidates {
		for _, candidate := range group.Candidates {
			item, ok := out[candidate.EvidenceID]
			if !ok || item.submitted || item.known {
				continue
			}
			seen := false
			for _, submittedID := range item.candidateFor {
				if submittedID == group.EvidenceID {
					seen = true
					break
				}
			}
			if !seen {
				item.candidateFor = append(item.candidateFor, group.EvidenceID)
				out[candidate.EvidenceID] = item
			}
		}
	}
	return out
}

func resolveSemanticAssessmentEvidenceConflictResults(
	req SemanticAssessmentRequest,
	response *SemanticAssessmentResponse,
) []SemanticValidationError {
	if response == nil {
		return []SemanticValidationError{semanticErr("evidence_conflict_results", "is required")}
	}
	if response.EvidenceConflictResults == nil {
		return []SemanticValidationError{semanticErr("evidence_conflict_results", "is required")}
	}
	if len(response.EvidenceConflictResults) > SemanticAssessmentMaxEvidenceConflictResults {
		return []SemanticValidationError{semanticErr("evidence_conflict_results", fmt.Sprintf("must contain at most %d entries", SemanticAssessmentMaxEvidenceConflictResults))}
	}
	citable := semanticAssessmentConflictCitableEvidence(req)
	reusedPairs := make(map[string]struct{})
	for _, equivalence := range response.EvidenceEquivalenceResults {
		if equivalence.Action != "reuse" || equivalence.CandidateEvidenceID == nil {
			continue
		}
		reusedPairs[equivalence.EvidenceID+"\x00"+strings.TrimSpace(*equivalence.CandidateEvidenceID)] = struct{}{}
	}
	seenCases := make(map[string]struct{}, len(response.EvidenceConflictResults))
	var errs []SemanticValidationError
	for index := range response.EvidenceConflictResults {
		result := &response.EvidenceConflictResults[index]
		field := fmt.Sprintf("evidence_conflict_results[%d]", index)
		if len(result.Positions) < 2 || len(result.Positions) > SemanticAssessmentMaxEvidenceConflictPositions {
			errs = append(errs, semanticErr(field+".positions", fmt.Sprintf("must contain between 2 and %d entries", SemanticAssessmentMaxEvidenceConflictPositions)))
		}
		seenPositions := make(map[string]struct{}, len(result.Positions))
		submittedIDs := make(map[string]struct{})
		caseParts := make([]string, 0, len(result.Positions))
		for positionIndex := range result.Positions {
			position := &result.Positions[positionIndex]
			positionField := fmt.Sprintf("%s.positions[%d]", field, positionIndex)
			if position.EvidenceID != strings.TrimSpace(position.EvidenceID) {
				errs = append(errs, semanticErr(positionField+".evidence_id", "must copy the supplied evidence_id exactly"))
				continue
			}
			if position.StartRef != strings.TrimSpace(position.StartRef) || position.EndRef != strings.TrimSpace(position.EndRef) {
				errs = append(errs, semanticErr(positionField, "must copy supplied boundary references exactly"))
				continue
			}
			item, ok := citable[position.EvidenceID]
			if !ok {
				errs = append(errs, semanticErr(positionField+".evidence_id", "is unknown or not citable"))
				continue
			}
			start, startOK := semanticAssessmentBoundaryOffset(item.evidence, position.StartRef)
			end, endOK := semanticAssessmentBoundaryOffset(item.evidence, position.EndRef)
			if !startOK || !endOK || start < 0 || end <= start {
				errs = append(errs, semanticErr(positionField, "contains invalid boundary references"))
				continue
			}
			quote, err := semanticExactSpanQuote(item.evidence.Content, start, end, "")
			if err != nil {
				errs = append(errs, semanticErr(positionField, err.Error()))
				continue
			}
			if len([]rune(quote)) > SemanticAssessmentMaxEvidenceConflictQuoteRunes {
				errs = append(errs, semanticErr(positionField, fmt.Sprintf("quote must contain at most %d runes", SemanticAssessmentMaxEvidenceConflictQuoteRunes)))
			}
			position.Start, position.End = start, end
			key := fmt.Sprintf("%s:%d:%d", position.EvidenceID, start, end)
			if _, duplicate := seenPositions[key]; duplicate {
				errs = append(errs, semanticErr(positionField, "duplicates another position"))
			}
			seenPositions[key] = struct{}{}
			caseParts = append(caseParts, key)
			if item.submitted {
				submittedIDs[position.EvidenceID] = struct{}{}
			}
		}
		if len(submittedIDs) == 0 {
			errs = append(errs, semanticErr(field+".positions", "must contain at least one submitted evidence position"))
		}
		for positionIndex, position := range result.Positions {
			item, ok := citable[position.EvidenceID]
			if !ok || item.submitted || item.known || len(item.candidateFor) == 0 {
				continue
			}
			associated := false
			for _, submittedID := range item.candidateFor {
				if _, exists := submittedIDs[submittedID]; exists {
					associated = true
					break
				}
			}
			if !associated {
				errs = append(errs, semanticErr(fmt.Sprintf("%s.positions[%d].evidence_id", field, positionIndex), "candidate is not associated with a submitted position in this result"))
			}
		}
		for positionIndex, position := range result.Positions {
			item, ok := citable[position.EvidenceID]
			if !ok || item.submitted || item.known {
				continue
			}
			for submittedID := range submittedIDs {
				if _, reused := reusedPairs[submittedID+"\x00"+position.EvidenceID]; reused {
					errs = append(errs, semanticErr(fmt.Sprintf("%s.positions[%d].evidence_id", field, positionIndex), "candidate is also selected for equivalence and cannot be cited as opposing"))
				}
			}
		}
		sort.Strings(caseParts)
		caseKey := strings.Join(caseParts, "|")
		if caseKey != "" {
			if _, duplicate := seenCases[caseKey]; duplicate {
				errs = append(errs, semanticErr(field, "duplicates another conflict result"))
			}
			seenCases[caseKey] = struct{}{}
		}
	}
	return errs
}
