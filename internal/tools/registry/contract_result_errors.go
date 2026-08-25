package registry

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func rememberToolResultError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	submissionID := uuid.NewString()
	var processErr *rememberapp.RememberProcessError
	if errors.As(err, &processErr) && processErr.Status != nil {
		submissionID = processErr.Status.SubmissionID
	}
	if errors.Is(err, rememberapp.ErrEvidenceSecurityRejected) {
		value := rememberapp.StatusError(rememberapp.SubmissionErrorQuarantined)
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
				"code":        value.Code,
				"message":     value.Message,
				"retryable":   value.Retryable,
				"next_action": value.NextAction,
				"remediation": value.Remediation,
			}},
		})
	}
	code := rememberErrorCode(err)
	if processErr != nil && processErr.Status != nil && len(processErr.Status.Errors) > 0 {
		code = rememberapp.SubmissionErrorCode(rememberapp.StatusErrorForCode(processErr.Status.Errors[0].Code, processErr.Status.ProcessingState).Code)
	}
	value := rememberapp.StatusError(code)
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    submissionID,
		"submission_kind":  "remember",
		"correlation_id":   correlation.FromContext(ctx),
		"errors": []any{map[string]any{
			"code": value.Code, "message": value.Message, "retryable": value.Retryable,
			"next_action": value.NextAction, "remediation": value.Remediation,
		}},
	})
}

func rememberErrorCode(err error) rememberapp.SubmissionErrorCode {
	switch {
	case errors.Is(err, memoryservice.ErrRememberConflict), errors.Is(err, rememberapp.ErrRememberConflict):
		return rememberapp.SubmissionErrorIdempotencyConflict
	case errors.Is(err, rememberapp.ErrRememberStaleInput):
		return rememberapp.SubmissionErrorStaleInput
	case errors.Is(err, rememberapp.ErrRememberEmbeddingUnavailable):
		return rememberapp.SubmissionErrorEmbeddingUnavailable
	case errors.Is(err, rememberapp.ErrRememberProviderUnavailable):
		return rememberapp.SubmissionErrorProviderUnavailable
	case errors.Is(err, rememberapp.ErrRememberProviderResponseInvalid):
		return rememberapp.SubmissionErrorProviderResponseInvalid
	case errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded):
		return rememberapp.SubmissionErrorInputBudgetExceeded
	case errors.Is(err, rememberapp.ErrRememberEmbeddingInvalid):
		return rememberapp.SubmissionErrorEmbeddingResponseInvalid
	case errors.Is(err, rememberapp.ErrRememberCommitConflict):
		return rememberapp.SubmissionErrorCommitConflict
	case errors.Is(err, rememberapp.ErrRememberRequestTimeout):
		return rememberapp.SubmissionErrorRequestTimeout
	case errors.Is(err, rememberapp.ErrRememberRequestCancelled):
		return rememberapp.SubmissionErrorRequestCancelled
	case errors.Is(err, rememberapp.ErrRememberProcessor), errors.Is(err, rememberapp.ErrRememberPersistence):
		return rememberapp.SubmissionErrorDatabaseFailure
	default:
		return rememberapp.SubmissionErrorInternalFailure
	}
}

func correctionToolResultError(ctx context.Context, err error) error {
	code := rememberapp.SubmissionErrorDatabaseFailure
	switch {
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingUnavailable):
		code = rememberapp.SubmissionErrorEmbeddingUnavailable
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingInvalid):
		code = rememberapp.SubmissionErrorEmbeddingResponseInvalid
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingTimeout):
		code = rememberapp.SubmissionErrorRequestTimeout
	}
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case httperr.NOT_FOUND:
			code = rememberapp.SubmissionErrorEntityNotFound
		case httperr.CONFLICT:
			code = rememberapp.SubmissionErrorRelationshipChanged
		}
	}
	value := rememberapp.StatusError(code)
	if code == rememberapp.SubmissionErrorDatabaseFailure {
		value.NextAction = string(rememberapp.SubmissionNextActionRetryCorrection)
		value.Remediation = "Retry correct_relationship with current relationship state and a new idempotency_key."
	}
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    uuid.NewString(),
		"submission_kind":  "relationship_correction",
		"correlation_id":   correlation.FromContext(ctx),
		"errors": []any{map[string]any{
			"code":        value.Code,
			"message":     value.Message,
			"retryable":   value.Retryable,
			"next_action": value.NextAction,
			"remediation": value.Remediation,
		}},
	})
}

func domainContractVersion() string {
	return domain.ContractVersion
}
