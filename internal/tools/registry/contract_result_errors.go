package registry

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func rememberToolResultError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	submissionID := uuid.NewString()
	if errors.Is(err, rememberapp.ErrEvidenceSecurityRejected) {
		return NewToolResultError(map[string]any{
			"contract_version":     domainContractVersion(),
			"submission_id":        submissionID,
			"submission_kind":      "remember",
			"processing_state":     "quarantined",
			"search_state":         "not_required",
			"correlation_id":       correlation.FromContext(ctx),
			"evidence":             []any{},
			"relationship_results": []any{},
			"errors": []any{map[string]any{
				"code":        string(memoryservice.SubmissionErrorQuarantined),
				"message":     "submission was quarantined by security policy",
				"retryable":   false,
				"next_action": "contact_operator",
				"remediation": "Contact an operator with submission_id and correlation_id.",
			}},
		})
	}
	code := memoryservice.SubmissionErrorInternalFailure
	retryable := true
	nextAction := string(memoryservice.SubmissionNextActionRetrySameRequest)
	remediation := "Retry the same request with the same idempotency_key after the transient failure clears."
	if errors.Is(err, memoryservice.ErrRememberConflict) {
		code = memoryservice.SubmissionErrorIdempotencyConflict
		retryable = true
		nextAction = string(memoryservice.SubmissionNextActionResubmitRemember)
		remediation = "Submit the complete batch again with a new idempotency_key after correcting the conflicting request."
	}
	if errors.Is(err, rememberapp.ErrRememberConflict) {
		code = memoryservice.SubmissionErrorIdempotencyConflict
		retryable = true
		nextAction = string(memoryservice.SubmissionNextActionResubmitRemember)
		remediation = "Submit the complete batch again with a new idempotency_key after correcting the conflicting request."
	}
	if errors.Is(err, rememberapp.ErrRememberStaleInput) {
		code = memoryservice.SubmissionErrorStaleInput
		retryable = true
		nextAction = string(memoryservice.SubmissionNextActionResubmitRemember)
		remediation = "Refresh the authoritative source or lifecycle state and submit the complete batch again with a new idempotency_key."
	}
	if errors.Is(err, rememberapp.ErrRememberEmbeddingUnavailable) {
		code = memoryservice.SubmissionErrorEmbeddingUnavailable
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key after the embedding provider recovers."
	}
	if errors.Is(err, rememberapp.ErrRememberProviderUnavailable) {
		code = memoryservice.SubmissionErrorProviderUnavailable
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key after the assessor provider recovers."
	}
	if errors.Is(err, rememberapp.ErrRememberProviderResponseInvalid) {
		code = memoryservice.SubmissionErrorProviderResponseInvalid
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key; the assessor response did not satisfy the closed contract."
	}
	if errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded) {
		code = memoryservice.SubmissionErrorInputBudgetExceeded
		retryable = false
		nextAction = string(memoryservice.SubmissionNextActionContactOperator)
		remediation = "Reduce the request or its candidate/document bound and submit the complete batch again."
	}
	if errors.Is(err, rememberapp.ErrRememberEmbeddingInvalid) {
		code = memoryservice.SubmissionErrorEmbeddingResponseInvalid
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key; the provider response did not match the active embedding contract."
	}
	if errors.Is(err, rememberapp.ErrRememberCommitConflict) {
		code = memoryservice.SubmissionErrorCommitConflict
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key after the server-owned state race clears."
	}
	if errors.Is(err, rememberapp.ErrRememberRequestTimeout) {
		code = memoryservice.SubmissionErrorRequestTimeout
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key; the bounded request deadline was reached."
	}
	if errors.Is(err, rememberapp.ErrRememberRequestCancelled) {
		code = memoryservice.SubmissionErrorRequestCancelled
		nextAction = string(memoryservice.SubmissionNextActionRetrySameRequest)
		remediation = "Retry the same request with the same idempotency_key after the cancelled request has been reissued."
	}
	if errors.Is(err, rememberapp.ErrRememberProcessor) || errors.Is(err, rememberapp.ErrRememberPersistence) {
		code = memoryservice.SubmissionErrorDatabaseFailure
	}
	value := rememberapp.StatusError(code)
	value.Retryable = retryable
	value.NextAction = nextAction
	value.Remediation = remediation
	message := value.Message
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    submissionID,
		"submission_kind":  "remember",
		"correlation_id":   correlation.FromContext(ctx),
		"errors": []any{map[string]any{
			"code": string(code), "message": message, "retryable": retryable,
			"next_action": nextAction, "remediation": remediation,
		}},
	})
}

func correctionToolResultError(ctx context.Context, err error) error {
	_ = err
	value := rememberapp.StatusError(memoryservice.SubmissionErrorInternalFailure)
	value.Retryable = true
	value.NextAction = string(memoryservice.SubmissionNextActionRetryCorrection)
	value.Remediation = "Retry correct_relationship with current relationship state and a new idempotency_key."
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    "",
		"submission_kind":  "relationship_correction",
		"correlation_id":   correlation.FromContext(ctx),
		"errors": []any{map[string]any{
			"code":        string(memoryservice.SubmissionErrorInternalFailure),
			"message":     "Dense-Mem could not complete the relationship correction",
			"retryable":   value.Retryable,
			"next_action": value.NextAction,
			"remediation": value.Remediation,
		}},
	})
}

func domainContractVersion() string {
	return domain.ContractVersion
}
