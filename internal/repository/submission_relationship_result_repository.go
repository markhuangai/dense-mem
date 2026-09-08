package repository

import "strings"

type submissionRelationshipAppliedSplit struct {
	RelationshipRef string
	SplitIndex      int
	Result          RelationshipDecisionResult
}

func submissionRelationshipNotStoredReasonAllowed(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "not_supported_by_evidence", "stale_input", "submission_policy_rejected", "security_quarantine", "internal_failure":
		return true
	default:
		return false
	}
}

func sortSubmissionRelationshipSplits(splits []SubmissionRelationshipSplitInput) {
	for i := 1; i < len(splits); i++ {
		for j := i; j > 0 && splits[j].SplitIndex < splits[j-1].SplitIndex; j-- {
			splits[j], splits[j-1] = splits[j-1], splits[j]
		}
	}
}

func sortSubmissionRelationshipResults(results []SubmissionRelationshipResultInput) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].RelationshipRef < results[j-1].RelationshipRef; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
}
