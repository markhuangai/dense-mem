package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func terminalRememberFailureResult(status *rememberapp.SubmissionStatusResult) (map[string]any, *rememberapp.TerminalRememberResult) {
	if status == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return map[string]any{}, nil
	}
	var publicResult map[string]any
	var terminal rememberapp.TerminalRememberResult
	if json.Unmarshal(encoded, &publicResult) != nil || json.Unmarshal(encoded, &terminal) != nil {
		return map[string]any{}, nil
	}
	terminal.Kind = rememberapp.ResultKindTerminal
	return publicResult, &terminal
}

func rememberConflictProcessError(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
) *rememberapp.RememberProcessError {
	if cause == nil {
		cause = rememberapp.ErrRememberConflict
	}
	evidence, relationshipResults := rememberFailureResults(input, "internal_failure")
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{rememberapp.StatusErrorWithDetails(rememberapp.SubmissionErrorIdempotencyConflict, "idempotency_conflict", map[string]any{"component": "remember.idempotency", "server_owned": true})},
	}
	return &rememberapp.RememberProcessError{Status: status, Err: cause}
}

func rememberFailureNotStoredReason(code rememberapp.SubmissionErrorCode) string {
	switch code {
	case rememberapp.SubmissionErrorPolicyRejected:
		return "submission_policy_rejected"
	case rememberapp.SubmissionErrorStaleInput:
		return "stale_input"
	default:
		return "internal_failure"
	}
}

func rememberFailureResults(
	input rememberapp.RememberProcessRequest,
	notStoredReason string,
) ([]rememberapp.SubmissionEvidenceStatus, []rememberapp.SubmissionRelationshipResult) {
	evidence := make([]rememberapp.SubmissionEvidenceStatus, len(input.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.SubmissionEvidenceStatus{
			Disposition:           "not_stored",
			ContentHash:           input.Evidence[index].ContentHash,
			EvidenceIndex:         index,
			SupersededEvidenceIDs: []string{},
			SearchState:           "not_required",
			Reason:                notStoredReason,
		}
	}
	refs := rememberFailureRelationshipRefs(input.Proposal)
	relationships := make([]rememberapp.SubmissionRelationshipResult, len(refs))
	for index, ref := range refs {
		relationships[index] = rememberapp.SubmissionRelationshipResult{
			RelationshipRef: ref,
			Disposition:     "not_stored",
			Reason:          notStoredReason,
			Splits:          []rememberapp.SubmissionRelationshipSplit{},
		}
	}
	return evidence, relationships
}

func rememberFailureRelationshipRefs(proposal map[string]any) []string {
	if proposal == nil {
		return []string{}
	}
	raw := proposal["relationship_hints"]
	if raw == nil {
		raw = proposal["relationships"]
	}
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []map[string]any:
		values = make([]any, 0, len(typed))
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		return []string{}
	}
	refs := make([]string, 0, len(values))
	for _, value := range values {
		fields, ok := value.(map[string]any)
		if !ok {
			refs = append(refs, "")
			continue
		}
		ref, _ := fields["ref"].(string)
		refs = append(refs, strings.TrimSpace(ref))
	}
	return refs
}

func rememberPreLockProcessError(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
) *rememberapp.RememberProcessError {
	code := rememberapp.TerminalErrorDatabaseFailure
	reasonCode := "idempotency_lock"
	if errors.Is(cause, context.DeadlineExceeded) {
		code = rememberapp.TerminalErrorRequestTimeout
		reasonCode = "idempotency_lock_timeout"
	} else if errors.Is(cause, context.Canceled) {
		code = rememberapp.TerminalErrorRequestCancelled
		reasonCode = "idempotency_lock_cancelled"
	}
	processErr := rememberFailureProcessErrorWithStatus(input, submissionID, cause, code, reasonCode, "remember.idempotency_lock")
	processErr.Err = cause
	return processErr
}
