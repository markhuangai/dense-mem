package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func terminalRememberFailureResult(status *SubmissionStatusResult) (map[string]any, *TerminalRememberResult) {
	if status == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return map[string]any{}, nil
	}
	var publicResult map[string]any
	var terminal TerminalRememberResult
	if json.Unmarshal(encoded, &publicResult) != nil || json.Unmarshal(encoded, &terminal) != nil {
		return map[string]any{}, nil
	}
	terminal.Kind = ResultKindTerminal
	return publicResult, &terminal
}

func rememberConflictProcessError(
	input RememberProcessRequest,
	submissionID string,
	cause error,
) *RememberProcessError {
	if cause == nil {
		cause = ErrRememberConflict
	}
	evidence := rememberFailureResults(input, TerminalNotStoredReasonForError(TerminalErrorIdempotencyConflict))
	status := &SubmissionStatusResult{
		SpaceKind:       input.SpaceKind,
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence,
		Errors:   []SubmissionStatusError{StatusErrorWithDetails(SubmissionErrorIdempotencyConflict, "idempotency_conflict", map[string]any{"component": "remember.idempotency", "server_owned": true})},
	}
	return &RememberProcessError{Status: status, Err: cause}
}

func rememberFailureResults(
	input RememberProcessRequest,
	notStoredReason string,
) []SubmissionEvidenceStatus {
	message, remediation := NotStoredGuidance(notStoredReason)
	evidence := make([]SubmissionEvidenceStatus, len(input.Evidence))
	for index := range evidence {
		evidence[index] = SubmissionEvidenceStatus{
			Disposition:           "not_stored",
			ContentHash:           input.Evidence[index].ContentHash,
			EvidenceIndex:         index,
			SupersededEvidenceIDs: []string{},
			SearchState:           "not_required",
			Reason:                notStoredReason,
			Message:               message,
			Remediation:           remediation,
		}
	}
	return evidence
}

func rememberPreLockProcessError(
	input RememberProcessRequest,
	submissionID string,
	cause error,
) *RememberProcessError {
	code := TerminalErrorDatabaseFailure
	reasonCode := "idempotency_lock"
	if errors.Is(cause, context.DeadlineExceeded) {
		code = TerminalErrorRequestTimeout
		reasonCode = "idempotency_lock_timeout"
	} else if errors.Is(cause, context.Canceled) {
		code = TerminalErrorRequestCancelled
		reasonCode = "idempotency_lock_cancelled"
	} else if errors.Is(cause, knowledgecontract.ErrRememberIdempotencyBusy) {
		reasonCode = "idempotency_lock_busy"
	}
	processErr := rememberFailureProcessErrorWithStatus(input, submissionID, cause, code, reasonCode, "remember.idempotency_lock")
	processErr.Err = cause
	return processErr
}
