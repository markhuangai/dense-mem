package remember

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func submissionStatusResultFromLedger(placement *StageResult) *SubmissionStatusResult {
	if placement == nil {
		return &SubmissionStatusResult{Evidence: []SubmissionEvidenceStatus{}, Errors: []SubmissionStatusError{}}
	}
	items := make([]SubmissionEvidenceStatus, 0, len(placement.Items))
	lineage := make(map[string][]string, len(placement.Evidence))
	for _, evidence := range placement.Evidence {
		lineage[evidence.FragmentID] = append([]string(nil), evidence.SupersededEvidenceIDs...)
	}
	searchState := string(domain.SearchProjectionNotRequired)
	searchErrorAdded := false
	processing := publicSubmissionProcessingState(placement.Status)
	statusErrors := make([]SubmissionStatusError, 0, 2)
	appendStatusError := func(value SubmissionStatusError) {
		for index := range statusErrors {
			existing := &statusErrors[index]
			if existing.Code != value.Code || existing.Message != value.Message {
				continue
			}
			mergeSubmissionResubmissionIssues(existing, value)
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
			searchError := submissionStatusErrorWithMessage(SubmissionErrorSearchIndexingDelayed, "Semantic search indexing is delayed.")
			itemError = &searchError
			searchErrorAdded = true
		}
		items = append(items, SubmissionEvidenceStatus{EvidenceID: item.FragmentID, EvidenceIndex: item.EvidenceIndex, SupersededEvidenceIDs: superseded, SearchState: itemSearchState, Error: itemError})
	}
	if processing == "failed" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorProcessingFailed))
	} else if processing == "quarantined" && len(statusErrors) == 0 {
		appendStatusError(submissionStatusError(SubmissionErrorQuarantined))
	}
	if searchErrorAdded {
		appendStatusError(submissionStatusErrorWithMessage(SubmissionErrorSearchIndexingDelayed, "Semantic search indexing is delayed; check the control portal for recovery guidance."))
	}
	result := &SubmissionStatusResult{
		SubmissionID: placement.SubmissionID, SubmissionKind: "remember", ProcessingState: processing, SearchState: searchState,
		CheckAfterSeconds: rememberCheckAfterSeconds, CorrelationID: placement.CorrelationID, SubmittedAt: placement.SubmittedAt,
		NextAttemptAt: placement.NextAttemptAt, StartedAt: placement.StartedAt, UpdatedAt: placement.UpdatedAt,
		CompletedAt: placement.CompletedAt, Evidence: items, Errors: statusErrors, QuarantineExpiresAt: placement.QuarantineExpiresAt,
	}
	if placement.MaxAttempts > 0 {
		attempts, maxAttempts := placement.Attempts, placement.MaxAttempts
		result.Attempts = &attempts
		result.MaxAttempts = &maxAttempts
	}
	return result
}

func mergeSubmissionResubmissionIssues(target *SubmissionStatusError, source SubmissionStatusError) {
	if target == nil {
		return
	}
	target.ResubmissionIssuesTruncated = target.ResubmissionIssuesTruncated || source.ResubmissionIssuesTruncated
	for _, candidate := range source.ResubmissionIssues {
		duplicate := false
		for _, existing := range target.ResubmissionIssues {
			if existing == candidate {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if len(target.ResubmissionIssues) >= submissionStatusMaxResubmissionIssues {
			target.ResubmissionIssuesTruncated = true
			break
		}
		target.ResubmissionIssues = append(target.ResubmissionIssues, candidate)
	}
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
