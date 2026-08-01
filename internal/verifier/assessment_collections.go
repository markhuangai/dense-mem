package verifier

import "fmt"

func assessmentCandidateGroupsBySpan(groups []SemanticAssessmentEntityCandidateGroup) map[string]SemanticAssessmentEntityCandidateGroup {
	out := make(map[string]SemanticAssessmentEntityCandidateGroup, len(groups))
	for _, group := range groups {
		out[assessmentSpanKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return out
}

func assessmentMatchingEntityCandidates(group SemanticAssessmentEntityCandidateGroup, kind string) []SemanticAssessmentEntityCandidate {
	matching := make([]SemanticAssessmentEntityCandidate, 0, len(group.Candidates))
	for _, candidate := range group.Candidates {
		if candidate.Kind == kind {
			matching = append(matching, candidate)
		}
	}
	return matching
}

func assessmentPredicateOptionsByKeyVersion(options []SemanticAssessmentPredicateOption) map[string]SemanticAssessmentPredicateOption {
	out := make(map[string]SemanticAssessmentPredicateOption, len(options))
	for _, option := range options {
		out[assessmentPredicateKey(option.PredicateKey, option.Version)] = option
	}
	return out
}

func assessmentSpanKey(evidenceID string, start int, end int) string {
	return fmt.Sprintf("%s:%d:%d", evidenceID, start, end)
}

func assessmentPredicateKey(key string, version int) string {
	return fmt.Sprintf("%s:%d", key, version)
}
