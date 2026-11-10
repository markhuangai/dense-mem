package postgres

import "github.com/markhuangai/dense-mem/internal/domain"

func rememberPublicResult(input SynchronousRememberCommitInput, evidence []EvidenceFragment, semantic *submissionSemanticCommitState) map[string]any {
	result := map[string]any{
		"space_kind":       input.Metadata["target_space"],
		"contract_version": domain.ContractVersion, "submission_id": input.IngestID,
		"submission_kind": "remember", "processing_state": "completed",
		"search_state": "not_required", "correlation_id": rememberCorrelationID(input.Metadata),
		"evidence": []map[string]any{},
		"errors":   []map[string]any{},
	}
	warnings := make([]string, 0, 2)
	if input.CandidateContextOmittedCandidates > 0 {
		warnings = append(warnings, "optional duplicate candidates were omitted to fit the configured assessor budget")
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
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
