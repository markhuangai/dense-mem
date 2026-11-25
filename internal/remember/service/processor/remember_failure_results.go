package processor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
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
	evidence := rememberFailureResults(input, rememberapp.TerminalNotStoredReasonForError(rememberapp.TerminalErrorIdempotencyConflict))
	status := &rememberapp.SubmissionStatusResult{
		SpaceKind:       input.SpaceKind,
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence,
		Errors:   []rememberapp.SubmissionStatusError{rememberapp.StatusErrorWithDetails(rememberapp.SubmissionErrorIdempotencyConflict, "idempotency_conflict", map[string]any{"component": "remember.idempotency", "server_owned": true})},
	}
	return &rememberapp.RememberProcessError{Status: status, Err: cause}
}

func rememberFailureResults(
	input rememberapp.RememberProcessRequest,
	notStoredReason string,
) []rememberapp.SubmissionEvidenceStatus {
	message, remediation := rememberapp.NotStoredGuidance(notStoredReason)
	evidence := make([]rememberapp.SubmissionEvidenceStatus, len(input.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.SubmissionEvidenceStatus{
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
	} else if errors.Is(cause, knowledgecontract.ErrRememberIdempotencyBusy) {
		reasonCode = "idempotency_lock_busy"
	}
	processErr := rememberFailureProcessErrorWithStatus(input, submissionID, cause, code, reasonCode, "remember.idempotency_lock")
	processErr.Err = cause
	return processErr
}
