package registry

import (
	"context"
	"errors"
	"strings"

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
	correlationID := correlation.FromContext(ctx)
	var processErr *rememberapp.RememberProcessError
	if errors.As(err, &processErr) && processErr.Status != nil {
		submissionID = processErr.Status.SubmissionID
		if durableCorrelationID := strings.TrimSpace(processErr.Status.CorrelationID); durableCorrelationID != "" {
			correlationID = durableCorrelationID
		}
	}
	if processErr != nil && processErr.Result != nil {
		if result, mapErr := structToMap(processErr.Result); mapErr == nil {
			return NewToolResultError(result)
		}
	}
	if errors.Is(err, rememberapp.ErrEvidenceSecurityRejected) {
		value := rememberapp.StatusError(rememberapp.SubmissionErrorQuarantined)
		return NewToolResultError(map[string]any{
			"contract_version":     domainContractVersion(),
			"submission_id":        submissionID,
			"submission_kind":      "remember",
			"processing_state":     "quarantined",
			"search_state":         "not_required",
			"correlation_id":       correlationID,
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
	processingState := "failed"
	switch code {
	case rememberapp.SubmissionErrorNoSupportedMemory, rememberapp.SubmissionErrorStaleInput:
		processingState = "rejected"
	case rememberapp.SubmissionErrorQuarantined:
		processingState = "quarantined"
	}
	return NewToolResultError(map[string]any{
		"contract_version":     domainContractVersion(),
		"submission_id":        submissionID,
		"submission_kind":      "remember",
		"processing_state":     processingState,
		"search_state":         "not_required",
		"correlation_id":       correlationID,
		"evidence":             []any{},
		"relationship_results": []any{},
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

func correctionToolResultError(ctx context.Context, submissionID string, err error) error {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		submissionID = uuid.NewString()
	}
	code := rememberapp.SubmissionErrorDatabaseFailure
	processingState := "failed"
	switch {
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingUnavailable):
		code = rememberapp.SubmissionErrorEmbeddingUnavailable
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingInvalid):
		code = rememberapp.SubmissionErrorEmbeddingResponseInvalid
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingTimeout):
		code = rememberapp.SubmissionErrorRequestTimeout
	case errors.Is(err, context.DeadlineExceeded):
		code = rememberapp.SubmissionErrorRequestTimeout
	case errors.Is(err, context.Canceled):
		code = rememberapp.SubmissionErrorRequestCancelled
	}
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case httperr.ErrEmbeddingUnavailable:
			code = rememberapp.SubmissionErrorEmbeddingUnavailable
		case httperr.ErrEmbeddingResponseInvalid:
			code = rememberapp.SubmissionErrorEmbeddingResponseInvalid
		case httperr.ErrEmbeddingTimeout:
			code = rememberapp.SubmissionErrorRequestTimeout
		case httperr.NOT_FOUND:
			code = rememberapp.SubmissionErrorEntityNotFound
		case httperr.CONFLICT:
			code, processingState = correctionConflictCode(apiErr)
		}
	}
	value := correctionStatusError(code)
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    submissionID,
		"submission_kind":  "relationship_correction",
		"processing_state": processingState,
		"search_state":     "not_required",
		"correlation_id":   rememberapp.NormalizeTerminalCorrelationID(correlation.FromContext(ctx)),
		"errors": []any{map[string]any{
			"code":        value.Code,
			"message":     value.Message,
			"retryable":   value.Retryable,
			"next_action": value.NextAction,
			"remediation": value.Remediation,
		}},
	})
}

func correctionConflictCode(apiErr *httperr.APIError) (rememberapp.SubmissionErrorCode, string) {
	switch {
	case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.SubmissionErrorIdempotencyConflict)):
		return rememberapp.SubmissionErrorIdempotencyConflict, "failed"
	case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.SubmissionErrorConfirmationExpired)):
		return rememberapp.SubmissionErrorConfirmationExpired, "rejected"
	case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.SubmissionErrorCommitConflict)):
		return rememberapp.SubmissionErrorCommitConflict, "failed"
	default:
		return rememberapp.SubmissionErrorRelationshipChanged, "failed"
	}
}

func correctionStatusError(code rememberapp.SubmissionErrorCode) rememberapp.SubmissionStatusError {
	value := rememberapp.StatusError(code)
	if code == rememberapp.SubmissionErrorDatabaseFailure || code == rememberapp.SubmissionErrorIdempotencyConflict {
		value.NextAction = string(rememberapp.SubmissionNextActionRetryCorrection)
		value.Remediation = "Retry correct_relationship with current relationship state and a new idempotency_key."
	}
	return value
}

func apiErrorDetailEquals(apiErr *httperr.APIError, field, value string) bool {
	if apiErr == nil {
		return false
	}
	for _, detail := range apiErr.Details {
		if strings.TrimSpace(detail.Field) == field && strings.TrimSpace(detail.Message) == value {
			return true
		}
	}
	return false
}

func domainContractVersion() string {
	return domain.ContractVersion
}
