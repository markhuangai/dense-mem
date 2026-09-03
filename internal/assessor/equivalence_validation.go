package assessor

import (
	"fmt"
	"strings"
)

func normalizeSemanticAssessmentSecurityResults(response *SemanticAssessmentResponse) {
	if response == nil {
		return
	}
	if len(response.EvidenceSecurityResults) == 0 && response.SecurityResults != nil {
		response.EvidenceSecurityResults = make([]SemanticAssessmentEvidenceSecurityResult, 0, len(response.SecurityResults))
		for _, result := range response.SecurityResults {
			response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, SemanticAssessmentEvidenceSecurityResult{
				EvidenceID: result.EvidenceID, Decision: result.Decision, Signals: append([]SemanticAssessmentSecuritySignal(nil), result.Signals...),
			})
		}
	}
	legacySignals := response.SecuritySignals
	for i := range response.EvidenceSecurityResults {
		result := &response.EvidenceSecurityResults[i]
		result.EvidenceID = strings.TrimSpace(result.EvidenceID)
		result.Decision = strings.TrimSpace(result.Decision)
		if result.Signals == nil {
			result.Signals = []SemanticAssessmentSecuritySignal{}
		}
		for j := range result.Signals {
			signal := &result.Signals[j]
			if strings.TrimSpace(signal.EvidenceID) == "" {
				signal.EvidenceID = result.EvidenceID
			}
			signal.EvidenceID = strings.TrimSpace(signal.EvidenceID)
			signal.Kind = strings.TrimSpace(signal.Kind)
			signal.StartRef = strings.TrimSpace(signal.StartRef)
			signal.EndRef = strings.TrimSpace(signal.EndRef)
		}
		if len(result.Signals) == 0 && len(legacySignals) > 0 {
			for _, signal := range legacySignals {
				if strings.TrimSpace(signal.EvidenceID) == result.EvidenceID {
					result.Signals = append(result.Signals, signal)
				}
			}
		}
	}
	response.SecuritySignals = nil
	response.SecurityResults = nil
}

func validateSemanticAssessmentEvidenceEquivalenceCandidates(req SemanticAssessmentRequest) []SemanticValidationError {
	evidenceByID := semanticEvidenceByID(req.Evidence)
	seenGroups := make(map[string]struct{}, len(req.EvidenceEquivalenceCandidates))
	var errs []SemanticValidationError
	for index, group := range req.EvidenceEquivalenceCandidates {
		field := fmt.Sprintf("evidence_equivalence_candidates[%d]", index)
		evidenceID := strings.TrimSpace(group.EvidenceID)
		if evidenceID == "" {
			errs = append(errs, semanticErr(field+".evidence_id", "is required"))
		} else if _, exists := seenGroups[evidenceID]; exists {
			errs = append(errs, semanticErr(field+".evidence_id", "is duplicated"))
		} else {
			seenGroups[evidenceID] = struct{}{}
			if _, exists := evidenceByID[evidenceID]; !exists {
				errs = append(errs, semanticErr(field+".evidence_id", "is unknown"))
			}
		}
		seenCandidates := make(map[string]struct{}, len(group.Candidates))
		for candidateIndex, candidate := range group.Candidates {
			candidateField := fmt.Sprintf("%s.candidates[%d]", field, candidateIndex)
			candidateID := strings.TrimSpace(candidate.EvidenceID)
			if candidateID == "" || len([]rune(candidateID)) > 128 {
				errs = append(errs, semanticErr(candidateField+".evidence_id", "is required and must be bounded"))
			}
			if strings.TrimSpace(candidate.Content) == "" {
				errs = append(errs, semanticErr(candidateField+".content", "is required"))
			}
			if _, exists := seenCandidates[candidateID]; exists {
				errs = append(errs, semanticErr(candidateField+".evidence_id", "is duplicated"))
			}
			seenCandidates[candidateID] = struct{}{}
		}
		if len(group.Candidates) > SemanticAssessmentMaxEvidenceEquivalenceCandidates {
			errs = append(errs, semanticErr(field+".candidates", fmt.Sprintf("must contain at most %d candidates", SemanticAssessmentMaxEvidenceEquivalenceCandidates)))
		}
	}
	return errs
}

func validateSemanticAssessmentEvidenceEquivalenceResults(
	req SemanticAssessmentRequest,
	results []SemanticAssessmentEvidenceEquivalenceResult,
) []SemanticValidationError {
	groups := make(map[string]SemanticAssessmentEvidenceEquivalenceCandidateGroup, len(req.EvidenceEquivalenceCandidates))
	var errs []SemanticValidationError
	for index, group := range req.EvidenceEquivalenceCandidates {
		group.EvidenceID = strings.TrimSpace(group.EvidenceID)
		if group.EvidenceID == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].evidence_id", index), "is required"))
			continue
		}
		if _, exists := groups[group.EvidenceID]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].evidence_id", index), "is duplicated"))
			continue
		}
		if _, exists := semanticEvidenceByID(req.Evidence)[group.EvidenceID]; !exists {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].evidence_id", index), "is unknown"))
		}
		seenCandidates := make(map[string]struct{}, len(group.Candidates))
		for candidateIndex, candidate := range group.Candidates {
			candidate.EvidenceID = strings.TrimSpace(candidate.EvidenceID)
			if candidate.EvidenceID == "" || len([]rune(candidate.EvidenceID)) > 128 {
				errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].candidates[%d].evidence_id", index, candidateIndex), "is required and must be bounded"))
			}
			if strings.TrimSpace(candidate.Content) == "" {
				errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].candidates[%d].content", index, candidateIndex), "is required"))
			}
			if _, exists := seenCandidates[candidate.EvidenceID]; exists {
				errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].candidates[%d].evidence_id", index, candidateIndex), "is duplicated"))
			}
			seenCandidates[candidate.EvidenceID] = struct{}{}
		}
		if len(group.Candidates) > SemanticAssessmentMaxEvidenceEquivalenceCandidates {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence_equivalence_candidates[%d].candidates", index), fmt.Sprintf("must contain at most %d candidates", SemanticAssessmentMaxEvidenceEquivalenceCandidates)))
		}
		groups[group.EvidenceID] = group
	}
	seenResults := make(map[string]struct{}, len(results))
	for index, result := range results {
		field := fmt.Sprintf("evidence_equivalence_results[%d]", index)
		result.EvidenceID = strings.TrimSpace(result.EvidenceID)
		if _, duplicate := seenResults[result.EvidenceID]; duplicate {
			errs = append(errs, semanticErr(field+".evidence_id", "is duplicated"))
		}
		seenResults[result.EvidenceID] = struct{}{}
		group, ok := groups[result.EvidenceID]
		if !ok {
			errs = append(errs, semanticErr(field+".evidence_id", "is not an allowlisted non-exact evidence item"))
			continue
		}
		switch result.Action {
		case "new":
			if result.CandidateEvidenceID != nil {
				errs = append(errs, semanticErr(field+".candidate_evidence_id", "must be null for new"))
			}
		case "reuse":
			candidateID := ""
			if result.CandidateEvidenceID != nil {
				candidateID = strings.TrimSpace(*result.CandidateEvidenceID)
			}
			if candidateID == "" {
				errs = append(errs, semanticErr(field+".candidate_evidence_id", "is required for reuse"))
				continue
			}
			found := false
			for _, candidate := range group.Candidates {
				if candidate.EvidenceID == candidateID {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, semanticErr(field+".candidate_evidence_id", "is outside the candidate allowlist"))
			}
		default:
			errs = append(errs, semanticErr(field+".action", "must be new or reuse"))
		}
	}
	if len(results) != len(groups) {
		errs = append(errs, semanticErr("evidence_equivalence_results", "must contain exactly one result per non-exact evidence item"))
	}
	for evidenceID := range groups {
		if _, ok := seenResults[evidenceID]; !ok {
			errs = append(errs, semanticErr("evidence_equivalence_results", "is missing a non-exact evidence result"))
		}
	}
	return errs
}
