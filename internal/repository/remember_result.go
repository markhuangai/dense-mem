package repository

import (
	"github.com/markhuangai/dense-mem/internal/domain"
)

// RememberTerminalErrorInput carries the application-owned terminal error
// projection into the persistence adapter without making the adapter own its
// public error policy.
type RememberTerminalErrorInput struct {
	Code        string
	Message     string
	Retryable   bool
	NextAction  string
	Remediation string
}

func rememberPublicResult(input SynchronousRememberCommitInput, evidence []EvidenceFragment, semantic *submissionSemanticCommitState, appliedSplits []submissionRelationshipAppliedSplit) map[string]any {
	result := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": input.IngestID,
		"submission_kind": "remember", "processing_state": "completed",
		"search_state": "not_required", "correlation_id": rememberCorrelationID(input.Metadata),
		"evidence": []map[string]any{}, "relationship_results": []map[string]any{},
		"errors": []map[string]any{},
	}
	if len(semantic.SearchDocuments) > 0 {
		result["search_state"] = string(domain.SearchProjectionCurrent)
	}
	evidenceResults := make([]map[string]any, 0, len(evidence))
	for _, item := range evidence {
		evidenceResults = append(evidenceResults, map[string]any{
			"disposition": "stored", "evidence_id": item.FragmentID, "evidence_index": item.EvidenceIndex,
			"superseded_evidence_ids": append([]string{}, item.SupersededEvidenceIDs...),
			"search_state":            result["search_state"],
		})
	}
	result["evidence"] = evidenceResults
	relationshipResults := make([]map[string]any, 0, len(input.Commit.RelationshipResults))
	byRef := make(map[string][]SubmissionRelationshipSplitInput)
	for _, applied := range appliedSplits {
		if applied.RelationshipRef == "" || applied.Result.Relationship == nil {
			continue
		}
		byRef[applied.RelationshipRef] = append(byRef[applied.RelationshipRef], SubmissionRelationshipSplitInput{
			SplitIndex: applied.SplitIndex, RelationshipID: applied.Result.Relationship.RelationshipID,
			RelationshipVersion: applied.Result.Relationship.Version, Status: applied.Result.Relationship.Status,
		})
	}
	for _, item := range input.Commit.RelationshipResults {
		entry := map[string]any{"ref": item.RelationshipRef, "disposition": item.Disposition, "splits": []map[string]any{}}
		if item.Reason != "" {
			entry["reason"] = item.Reason
		}
		if splits := byRef[item.RelationshipRef]; len(splits) > 0 {
			entry["disposition"] = "stored"
			entry["splits"] = splits
		}
		relationshipResults = append(relationshipResults, entry)
	}
	result["relationship_results"] = relationshipResults
	return result
}

func rememberTerminalPublicResult(input SynchronousRememberCommitInput, evidence []EvidenceFragment, outcome string, terminalError RememberTerminalErrorInput) map[string]any {
	result := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": input.IngestID,
		"submission_kind": "remember", "processing_state": outcome, "search_state": string(domain.SearchProjectionNotRequired),
		"correlation_id": rememberCorrelationID(input.Metadata), "evidence": []map[string]any{},
		"relationship_results": []map[string]any{}, "errors": []map[string]any{},
	}
	evidenceResults := make([]map[string]any, 0, len(evidence))
	disposition, reason := "not_stored", "not_supported_by_evidence"
	if outcome == "quarantined" {
		reason = "security_quarantine"
	}
	for _, item := range evidence {
		evidenceResults = append(evidenceResults, map[string]any{
			"disposition": disposition, "evidence_index": item.EvidenceIndex,
			"superseded_evidence_ids": []string{},
			"search_state":            string(domain.SearchProjectionNotRequired), "reason": reason,
		})
	}
	result["evidence"] = evidenceResults
	rels := make([]map[string]any, 0, len(input.Commit.RelationshipResults))
	for _, item := range input.Commit.RelationshipResults {
		reason := item.Reason
		if reason == "" {
			reason = "not_supported_by_evidence"
			if outcome == "quarantined" {
				reason = "security_quarantine"
			}
		}
		rel := map[string]any{"ref": item.RelationshipRef, "disposition": "not_stored", "reason": reason, "splits": []map[string]any{}}
		rels = append(rels, rel)
	}
	result["relationship_results"] = rels
	result["errors"] = []map[string]any{{
		"code": terminalError.Code, "message": terminalError.Message, "retryable": terminalError.Retryable,
		"next_action": terminalError.NextAction, "remediation": terminalError.Remediation,
	}}
	return result
}
