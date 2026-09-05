package registry

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

const (
	actionRetrySameRequest = "retry_same_request"
	actionCorrectInput     = "correct_and_resubmit"
	actionRefreshState     = "refresh_state"
	actionAuthorization    = "obtain_authorization"
	actionContactOperator  = "contact_operator"
	actionStop             = "stop"
)

// ActionableErrorData is the single MCP-facing projection for errors that do
// not already carry a structured tool result. It deliberately uses a closed
// set of safe messages and never serializes err.Error().
func ActionableErrorData(ctx context.Context, tool string, err error) map[string]any {
	tool = boundedContractText(strings.TrimSpace(tool), 128)
	code := domain.ErrorProviderUnavailable
	reasonCode := "tool_execution_failed"
	message := "Dense-Mem could not complete the " + tool + " operation."
	retryable := false
	nextAction := actionContactOperator
	remediation := "Contact an operator with the correlation ID and affected tool."
	var apiErr *httperr.APIError
	failureDetails := map[string]any{}

	if err == nil {
		err = errors.New("unknown tool failure")
	}
	switch {
	case errors.Is(err, context.Canceled):
		code, reasonCode, message, nextAction, remediation = domain.ErrorConflict, "request_cancelled", "The "+tool+" operation was cancelled before completion.", actionStop, "Stop this operation; retry only if the caller still needs it."
	case errors.Is(err, context.DeadlineExceeded):
		code, reasonCode, message, retryable, nextAction, remediation = domain.ErrorProviderUnavailable, "request_timeout", "The "+tool+" operation exceeded its bounded deadline.", true, actionRetrySameRequest, actionableTimeoutRemediation(tool)
	case errors.Is(err, ErrToolUnavailable):
		code, reasonCode, message, nextAction, remediation = domain.ErrorDegraded, "tool_unavailable", "The "+tool+" operation is not available on this server.", actionContactOperator, "Contact an operator to enable the required server capability, then retry."
	case errors.Is(err, rememberapp.ErrRememberAuthContext), errors.Is(err, memoryservice.ErrLifecycleAuthContext), errors.Is(err, contextservice.ErrTraceAuthContext), errors.Is(err, dreamservice.ErrDreamAuthContext), errors.Is(err, skillpackservice.ErrMemoryPackAuthContext):
		code, reasonCode, message, nextAction, remediation = domain.ErrorUnauthorizedScope, "authenticated_context_required", "Dense-Mem could not authorize the "+tool+" operation.", actionAuthorization, "Authenticate with a credential that has access to this tool and retry."
	case errors.Is(err, repository.ErrTraceRelationshipNotFound), errors.Is(err, contextservice.ErrTraceRelationshipNotFound), errors.Is(err, dreamservice.ErrDreamNotFound), errors.Is(err, repository.ErrDreamHypothesisNotFound):
		code, reasonCode, message, nextAction, remediation = domain.ErrorInvalidInput, "reference_not_found", "The reference supplied to "+tool+" was not found or is no longer available.", actionRefreshState, "Refresh authorized state, then retry with a current reference."
	case errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded):
		code, reasonCode, message, nextAction, remediation = domain.ErrorInvalidInput, "input_budget_exceeded", "The assessor input for "+tool+" exceeds the configured server budget.", actionContactOperator, "Ask an operator to review the configured assessor budget and selected server context before retrying."
		if measuredReason, measured := memoryservice.SynchronousAssessmentFailureDetails(err); measuredReason != "" {
			reasonCode = measuredReason
			for key, value := range measured {
				failureDetails[key] = value
			}
			if clientControlled, ok := measured["client_controlled"].(bool); ok && clientControlled {
				message = "The submitted evidence and request context exceed the configured assessor input budget."
				nextAction = actionCorrectInput
				remediation = "Reduce the submitted evidence or relationship detail, then submit the corrected request with a new idempotency key."
			}
		}
	case errors.Is(err, rememberapp.ErrRememberStaleInput), errors.Is(err, rememberapp.ErrRememberCommitConflict):
		code, reasonCode, message, nextAction, remediation = domain.ErrorConflict, "stale_state", "Server-owned state changed while "+tool+" was running.", actionRefreshState, "Refresh the current state and resubmit with a new idempotency key."
	case errors.Is(err, rememberapp.ErrRememberRequestTimeout):
		code, reasonCode, message, retryable, nextAction, remediation = domain.ErrorProviderUnavailable, "request_timeout", "The "+tool+" operation exceeded its bounded deadline.", true, actionRetrySameRequest, actionableTimeoutRemediation(tool)
	case errors.Is(err, rememberapp.ErrRememberRequestCancelled):
		code, reasonCode, message, nextAction, remediation = domain.ErrorConflict, "request_cancelled", "The "+tool+" operation was cancelled before completion.", actionStop, "Stop this operation; retry only if the caller still needs it."
	case errors.Is(err, rememberapp.ErrRememberConflict), errors.Is(err, repository.ErrIdempotencyConflict):
		code, reasonCode, message, nextAction, remediation = domain.ErrorConflict, "idempotency_conflict", "The idempotency key for "+tool+" is already bound to a different request.", actionRefreshState, "Reuse the key only for the original request; otherwise submit the changed request with a new key."
	case actionableInputBudgetError(err):
		code, reasonCode, message, nextAction, remediation = domain.ErrorInvalidInput, "assessor_input_budget_exceeded", "The server could not fit the assessor conversation within its configured input budget.", actionContactOperator, "Ask an operator to review the configured assessor budget and server-owned context before retrying."
		if _, measured := memoryservice.SynchronousAssessmentFailureDetails(err); measured != nil {
			for key, value := range measured {
				failureDetails[key] = value
			}
		}
	case errors.Is(err, rememberapp.ErrRememberProviderResponseInvalid), errors.Is(err, modelprovider.ErrVerifierMalformedResponse):
		code, reasonCode, message, retryable, nextAction, remediation = domain.ErrorProviderMalformed, "provider_response_invalid", "The "+tool+" operation received an unusable structured response.", true, actionRetrySameRequest, actionableTransientRemediation(tool)
	case errors.Is(err, rememberapp.ErrRememberProviderUnavailable), errors.Is(err, rememberapp.ErrRememberEmbeddingUnavailable), errors.Is(err, modelprovider.ErrVerifierProvider), errors.Is(err, modelprovider.ErrVerifierRateLimit), errors.Is(err, modelprovider.ErrVerifierTimeout):
		code, reasonCode, message, retryable, nextAction, remediation = domain.ErrorProviderUnavailable, "provider_unavailable", "A required service for "+tool+" is temporarily unavailable.", true, actionRetrySameRequest, actionableTransientRemediation(tool)
	case errors.As(err, &apiErr) && apiErr != nil:
		status := httperr.HTTPStatusCode(apiErr.Code)
		message = boundedContractText(apiErr.Message, 512)
		switch {
		case status == 401 || status == 403:
			code, reasonCode, nextAction, remediation = domain.ErrorUnauthorizedScope, "authorization_required", actionAuthorization, "Obtain the required authorization or scope, then retry."
		case status == 404:
			code, reasonCode, nextAction, remediation = domain.ErrorInvalidInput, "reference_not_found", actionRefreshState, "Refresh authorized state and retry with a current reference."
		case status == 409:
			code, reasonCode, nextAction, remediation = domain.ErrorConflict, "state_conflict", actionRefreshState, "Refresh authoritative state and retry with current values."
		case status == 429:
			code, reasonCode, retryable, nextAction, remediation = domain.ErrorProviderUnavailable, "rate_limited", true, actionRetrySameRequest, actionableTransientRemediation(tool)
		case status >= 500:
			code, reasonCode, retryable, nextAction, remediation = domain.ErrorProviderUnavailable, "service_unavailable", true, actionRetrySameRequest, actionableTransientRemediation(tool)
		default:
			code, reasonCode, nextAction, remediation = domain.ErrorInvalidInput, "invalid_request", actionCorrectInput, "Correct the identified request fields and submit again."
		}
	}

	details := map[string]any{}
	for key, value := range failureDetails {
		details[key] = value
	}
	if tool != "" {
		details["tool"] = tool
	}
	if provider := modelprovider.ProviderFailureDetails(err); provider.RetryAfter > 0 {
		details["retry_after_seconds"] = int(provider.RetryAfter.Seconds())
	}
	result := map[string]any{
		"code":        string(code),
		"reason_code": boundedContractText(reasonCode, 128),
		"message":     boundedContractText(message, 512),
		"retryable":   retryable,
		"next_action": nextAction,
		"remediation": boundedContractText(remediation, 512),
		"details":     details,
	}
	if seconds, ok := details["retry_after_seconds"].(int); ok && seconds > 0 {
		result["retry_after_seconds"] = seconds
	}
	correlationID := strings.TrimSpace(correlation.FromContext(ctx))
	if correlationID == "" {
		correlationID = uuid.NewString()
	}
	result["correlation_id"] = correlationID
	return result
}

// ActionableInvalidInputData creates the bounded projection for a transport
// parser failure before a typed capability error exists.
func ActionableInvalidInputData(ctx context.Context, tool, reasonCode, message, remediation string) map[string]any {
	result := ActionableErrorData(ctx, tool, errors.New("invalid input"))
	result["code"] = string(domain.ErrorInvalidInput)
	result["reason_code"] = boundedContractText(reasonCode, 128)
	result["message"] = boundedContractText(message, 512)
	result["retryable"] = false
	result["next_action"] = actionCorrectInput
	result["remediation"] = boundedContractText(remediation, 512)
	return result
}

func actionableInputBudgetError(err error) bool {
	var malformed *modelprovider.MalformedResponseError
	return errors.As(err, &malformed) && malformed != nil && strings.TrimSpace(malformed.FailureClass) == "input_budget"
}

func ActionableAuthorizationData(ctx context.Context, tool string) map[string]any {
	return ActionableErrorData(ctx, tool, httperr.New(httperr.FORBIDDEN, "insufficient permissions"))
}

func ActionableToolUnavailableData(ctx context.Context, tool string) map[string]any {
	return ActionableErrorData(ctx, tool, ErrToolUnavailable)
}

func ActionableSerializationFailureData(ctx context.Context, tool string) map[string]any {
	result := ActionableToolUnavailableData(ctx, tool)
	result["reason_code"] = "tool_result_serialization_failed"
	result["message"] = "Dense-Mem could not serialize the result for this operation."
	result["remediation"] = "Contact an operator with the correlation ID and affected tool."
	return result
}

func actionableTimeoutRemediation(tool string) string {
	if actionableToolRequiresIdempotency(tool) {
		return "Retry the same request with the same idempotency key after the timeout clears."
	}
	if actionableToolIsRead(tool) {
		return "Retry the read request with the same arguments after the timeout clears."
	}
	return "Retry the operation with the same arguments after the timeout clears."
}

func actionableTransientRemediation(tool string) string {
	if actionableToolRequiresIdempotency(tool) {
		return "Retry the same request with the same idempotency key after the transient failure clears."
	}
	if actionableToolIsRead(tool) {
		return "Retry the read request with the same arguments after the service recovers."
	}
	return "Retry the operation with the same arguments after the service recovers."
}

func actionableToolRequiresIdempotency(tool string) bool {
	switch strings.TrimSpace(tool) {
	case ToolRemember, ToolRetractEvidence, ToolCorrectRelationship, ToolSubmitRecallSessionFeedback, ToolResolveDreamFeedback:
		return true
	default:
		return false
	}
}

func actionableToolIsRead(tool string) bool {
	switch strings.TrimSpace(tool) {
	case ToolRecallMemory, ToolTraceMemory, ToolListDreams, ToolGetDream, ToolExportMemoryPack:
		return true
	default:
		return false
	}
}

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
	code := rememberErrorCode(err)
	if errors.Is(err, rememberapp.ErrEvidenceSecurityRejected) || errors.Is(err, rememberapp.ErrEncodedEvidenceNotAllowed) {
		code = rememberapp.SubmissionErrorPolicyRejected
	}
	if processErr != nil && processErr.Status != nil && len(processErr.Status.Errors) > 0 {
		code = rememberapp.SubmissionErrorCode(rememberapp.StatusErrorForCode(processErr.Status.Errors[0].Code, processErr.Status.ProcessingState).Code)
	}
	reasonCode, details := memoryservice.SynchronousAssessmentFailureDetails(err)
	if reasonCode == "" {
		reasonCode = "remember_failure"
		details = map[string]any{"component": "remember", "server_owned": true}
	}
	value := rememberapp.StatusErrorWithDetails(code, reasonCode, details)
	processingState := "failed"
	errorPayload := map[string]any{
		"code": value.Code, "message": value.Message, "retryable": value.Retryable,
		"next_action": value.NextAction, "remediation": value.Remediation,
		"reason_code": value.ReasonCode, "details": value.Details,
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
		"errors":               []any{errorPayload},
	})
}

func rememberErrorCode(err error) rememberapp.SubmissionErrorCode {
	switch {
	case errors.Is(err, memoryservice.ErrRememberConflict), errors.Is(err, rememberapp.ErrRememberConflict):
		return rememberapp.SubmissionErrorIdempotencyConflict
	case errors.Is(err, rememberapp.ErrRememberPolicyRejected), errors.Is(err, rememberapp.ErrEvidenceSecurityRejected), errors.Is(err, rememberapp.ErrEncodedEvidenceNotAllowed):
		return rememberapp.SubmissionErrorPolicyRejected
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
	reasonCode := "relationship_correction_failed"
	processingState := "failed"
	switch {
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingUnavailable):
		code = rememberapp.SubmissionErrorEmbeddingUnavailable
		reasonCode = "embedding_unavailable"
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingInvalid):
		code = rememberapp.SubmissionErrorEmbeddingResponseInvalid
		reasonCode = "embedding_response_invalid"
	case errors.Is(err, memoryservice.ErrLifecycleEmbeddingTimeout):
		code = rememberapp.SubmissionErrorRequestTimeout
		reasonCode = "embedding_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		code = rememberapp.SubmissionErrorRequestTimeout
		reasonCode = "request_timeout"
	case errors.Is(err, context.Canceled):
		code = rememberapp.SubmissionErrorRequestCancelled
		reasonCode = "request_cancelled"
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
			reasonCode = "relationship_correction_conflict"
		}
	}
	value := correctionStatusError(code)
	value.ReasonCode = reasonCode
	value.Details = map[string]any{"component": "relationship_correction", "server_owned": true}
	errorPayload := map[string]any{
		"code": value.Code, "message": value.Message, "retryable": value.Retryable,
		"next_action": value.NextAction, "remediation": value.Remediation,
		"reason_code": value.ReasonCode, "details": value.Details,
	}
	return NewToolResultError(map[string]any{
		"contract_version": domainContractVersion(),
		"submission_id":    submissionID,
		"submission_kind":  "relationship_correction",
		"processing_state": processingState,
		"search_state":     "not_required",
		"correlation_id":   rememberapp.NormalizeTerminalCorrelationID(correlation.FromContext(ctx)),
		"errors":           []any{errorPayload},
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
	if code == rememberapp.SubmissionErrorIdempotencyConflict {
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
