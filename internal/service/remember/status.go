package remember

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func submissionStatusResultFromLedger(placement *StageResult) *SubmissionStatusResult {
	if placement == nil {
		return &SubmissionStatusResult{
			ContractVersion: domain.ContractVersion,
			Kind:            ResultKindLegacyReceipt,
			Evidence:        []SubmissionEvidenceStatus{},
			Errors:          []SubmissionStatusError{},
			Degradations:    []SubmissionStatusDegradation{},
		}
	}
	items := make([]SubmissionEvidenceStatus, 0, len(placement.Items))
	lineage := make(map[string][]string, len(placement.Evidence))
	for _, evidence := range placement.Evidence {
		lineage[evidence.FragmentID] = append([]string(nil), evidence.SupersededEvidenceIDs...)
	}
	searchState := string(domain.SearchProjectionNotRequired)
	processing := publicSubmissionProcessingState(placement.Status)
	statusErrors := make([]SubmissionStatusError, 0, 2)
	degradations := make([]SubmissionStatusDegradation, 0, 1)
	appendStatusError := func(value SubmissionStatusError) {
		for index := range statusErrors {
			existing := &statusErrors[index]
			if existing.Code != value.Code || existing.Message != value.Message {
				continue
			}
			return
		}
		statusErrors = append(statusErrors, value)
	}
	for _, item := range placement.Items {
		itemSearchState := placementItemSearchState(item)
		searchState = placementCombinedSearchState(searchState, itemSearchState)
		superseded := lineage[item.FragmentID]
		if superseded == nil {
			superseded = []string{}
		}
		var itemError *SubmissionStatusError
		if semanticError := submissionItemFailureError(item, processing); semanticError != nil {
			itemError = semanticError
			appendStatusError(*semanticError)
		} else if itemSearchState == string(domain.SearchProjectionFailed) {
			if len(degradations) == 0 {
				degradations = append(degradations, SubmissionStatusDegradation{
					Frontier: "search",
					Optional: true,
					Code:     string(SubmissionErrorSearchIndexingDelayed),
					Message:  "Semantic search indexing is delayed; canonical memory remains committed.",
				})
			}
		}
		items = append(items, SubmissionEvidenceStatus{EvidenceID: item.FragmentID, EvidenceIndex: item.EvidenceIndex, SupersededEvidenceIDs: superseded, SearchState: itemSearchState, Error: itemError})
	}
	if processing == "rejected" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorNoSupportedMemory))
	} else if processing == "failed" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorProcessingFailed))
	} else if processing == "quarantined" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorQuarantined))
	}
	result := &SubmissionStatusResult{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    placement.SubmissionID, SubmissionKind: "remember", ProcessingState: processing, SearchState: searchState,
		CheckAfterSeconds: rememberCheckAfterSeconds, CorrelationID: placement.CorrelationID, SubmittedAt: placement.SubmittedAt,
		NextAttemptAt: placement.NextAttemptAt, StartedAt: placement.StartedAt, UpdatedAt: placement.UpdatedAt,
		CompletedAt: placement.CompletedAt, Evidence: items, Errors: statusErrors, Degradations: degradations,
		QuarantineExpiresAt: placement.QuarantineExpiresAt,
		Kind:                ResultKindLegacyReceipt,
	}
	for _, item := range placement.RelationshipResults {
		copy := SubmissionRelationshipResult{
			RelationshipRef: item.RelationshipRef,
			Disposition:     item.Disposition,
			Reason:          item.Reason,
			Splits:          make([]SubmissionRelationshipSplit, 0, len(item.Splits)),
		}
		for _, split := range item.Splits {
			copy.Splits = append(copy.Splits, SubmissionRelationshipSplit{
				SplitIndex: split.SplitIndex, RelationshipID: split.RelationshipID,
				RelationshipVersion: split.RelationshipVersion, Status: split.Status,
			})
		}
		result.RelationshipResults = append(result.RelationshipResults, copy)
	}
	if placement.MaxAttempts > 0 {
		attempts, maxAttempts := placement.Attempts, placement.MaxAttempts
		result.Attempts = &attempts
		result.MaxAttempts = &maxAttempts
	}
	return result
}

func ProjectSubmissionStatus(placement *StageResult) *SubmissionStatusResult {
	return submissionStatusResultFromLedger(placement)
}

func placementCombinedSearchState(left, right string) string {
	if left == string(domain.SearchProjectionFailed) || right == string(domain.SearchProjectionFailed) {
		return string(domain.SearchProjectionFailed)
	}
	if left == string(domain.SearchProjectionPending) || right == string(domain.SearchProjectionPending) {
		return string(domain.SearchProjectionPending)
	}
	if left == string(domain.SearchProjectionCurrent) || right == string(domain.SearchProjectionCurrent) {
		return string(domain.SearchProjectionCurrent)
	}
	return string(domain.SearchProjectionNotRequired)
}

func placementItemSearchState(item PlacementItem) string {
	if state := placementSearchStateFromStates(resultArray(item.Result, "search_document_states")); state != "" {
		return state
	}
	if len(resultArray(item.Result, "embedding_job_ids")) > 0 {
		return string(domain.SearchProjectionPending)
	}
	if len(resultArray(item.Result, "search_document_ids")) > 0 {
		return string(domain.SearchProjectionCurrent)
	}
	return string(domain.SearchProjectionNotRequired)
}

func placementSearchStateFromStates(values []any) string {
	if len(values) == 0 {
		return ""
	}
	hasCurrent := false
	for _, value := range values {
		state := strings.TrimSpace(fmt.Sprint(value))
		switch state {
		case string(domain.SearchProjectionFailed):
			return string(domain.SearchProjectionFailed)
		case string(domain.SearchProjectionPending):
			return string(domain.SearchProjectionPending)
		case string(domain.SearchProjectionCurrent):
			hasCurrent = true
		}
	}
	if hasCurrent {
		return string(domain.SearchProjectionCurrent)
	}
	return ""
}

func resultArray(result map[string]any, key string) []any {
	if len(result) == 0 {
		return nil
	}
	switch values := result[key].(type) {
	case []any:
		return values
	case []string:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	default:
		return nil
	}
}
