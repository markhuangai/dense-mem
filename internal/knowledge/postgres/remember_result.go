package postgres

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func rememberPublicResult(input SynchronousRememberCommitInput, evidence []EvidenceFragment, semantic *submissionSemanticCommitState) *knowledge.RememberReceipt {
	target, _ := input.Metadata["target_space"].(domain.MemorySpaceKind)
	if explicit, ok := input.Metadata["target_space"].(string); ok {
		target = domain.MemorySpaceKind(explicit)
	}
	result := &knowledge.RememberReceipt{
		SpaceKind: domain.MemorySpaceKind(target), ContractVersion: domain.ContractVersion, SubmissionID: input.IngestID,
		SubmissionKind: "remember", ProcessingState: "completed", SearchState: "not_required", CorrelationID: rememberCorrelationID(input.Metadata),
		Evidence: make([]knowledge.SubmissionEvidenceStatus, 0, len(evidence)), Errors: []knowledge.SubmissionStatusError{},
	}
	if input.CandidateContextOmittedCandidates > 0 {
		result.Warnings = []string{"optional duplicate candidates were omitted to fit the configured assessor budget"}
	}
	if len(semantic.SearchDocuments) > 0 || rememberHasReusedEvidence(evidence) {
		result.SearchState = string(domain.SearchProjectionCurrent)
	}
	for _, item := range evidence {
		entry := knowledge.SubmissionEvidenceStatus{
			Disposition: "stored", EvidenceID: item.FragmentID, EvidenceIndex: item.EvidenceIndex, ContentHash: item.ContentHash,
			SupersededEvidenceIDs: append([]string{}, item.SupersededEvidenceIDs...), SearchState: result.SearchState,
		}
		if p := item.Partition; p != nil {
			entry.EvidenceIndex, entry.PartitionIndex = p.SubmittedIndex, &p.Index
			entry.SourceItemID, entry.SpanStart, entry.SpanEnd = p.SourceItemID, &p.Start, &p.End
			entry.OccurrenceID = item.OccurrenceID
		}
		result.Evidence = append(result.Evidence, entry)
	}
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
