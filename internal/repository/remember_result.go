package repository

import "github.com/markhuangai/dense-mem/internal/domain"

func rememberPublicResult(input SynchronousRememberCommitInput, evidence []EvidenceFragment, semantic *submissionSemanticCommitState, appliedSplits []submissionRelationshipAppliedSplit) map[string]any {
	result := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": input.IngestID,
		"submission_kind": "remember", "processing_state": "completed",
		"search_state": "not_required", "correlation_id": rememberCorrelationID(input.Metadata),
		"evidence": []map[string]any{}, "relationship_results": []map[string]any{},
		"errors": []map[string]any{},
	}
	if len(semantic.SearchDocuments) > 0 || rememberHasReusedEvidence(evidence) {
		result["search_state"] = string(domain.SearchProjectionCurrent)
	}
	evidenceResults := make([]map[string]any, 0, len(evidence))
	for _, item := range evidence {
		evidenceResults = append(evidenceResults, map[string]any{
			"disposition": "stored", "evidence_id": item.FragmentID, "evidence_index": item.EvidenceIndex,
			"content_hash":            item.ContentHash,
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

func rememberHasReusedEvidence(evidence []EvidenceFragment) bool {
	for _, item := range evidence {
		if item.FragmentID != "" && item.FragmentID != item.SubmittedFragmentID {
			return true
		}
	}
	return false
}
