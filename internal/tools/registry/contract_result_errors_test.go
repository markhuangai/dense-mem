package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func TestActionableErrorDataMapsSupportedFailuresToRecoveryGuidance(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "actionable-correlation")
	cases := []struct {
		name       string
		tool       string
		err        error
		code       string
		reasonCode string
		nextAction string
		retryable  bool
	}{
		{name: "unknown", tool: "", err: nil, code: string(domain.ErrorProviderUnavailable), reasonCode: "tool_execution_failed", nextAction: actionContactOperator},
		{name: "cancelled", tool: ToolRemember, err: context.Canceled, code: string(domain.ErrorConflict), reasonCode: "request_cancelled", nextAction: actionStop},
		{name: "deadline read", tool: ToolRecallMemory, err: context.DeadlineExceeded, code: string(domain.ErrorProviderUnavailable), reasonCode: "request_timeout", nextAction: actionRetrySameRequest, retryable: true},
		{name: "unavailable", tool: ToolRecallMemory, err: ErrToolUnavailable, code: string(domain.ErrorDegraded), reasonCode: "tool_unavailable", nextAction: actionContactOperator},
		{name: "authorization", tool: ToolRemember, err: rememberapp.ErrRememberAuthContext, code: string(domain.ErrorUnauthorizedScope), reasonCode: "authenticated_context_required", nextAction: actionAuthorization},
		{name: "reference", tool: ToolTraceMemory, err: repository.ErrTraceRelationshipNotFound, code: string(domain.ErrorInvalidInput), reasonCode: "reference_not_found", nextAction: actionRefreshState},
		{name: "dream feedback input", tool: ToolResolveDreamFeedback, err: dreamservice.ErrDreamFeedbackInvalidInput, code: string(domain.ErrorInvalidInput), reasonCode: "invalid_request", nextAction: actionCorrectInput},
		{name: "read repository", tool: ToolRecallMemory, err: errors.New("database unavailable"), code: string(domain.ErrorProviderUnavailable), reasonCode: "read_unavailable", nextAction: actionRetrySameRequest, retryable: true},
		{name: "retract repository", tool: ToolRetractEvidence, err: errors.New("database unavailable"), code: string(domain.ErrorProviderUnavailable), reasonCode: "write_unavailable", nextAction: actionRetrySameRequest, retryable: true},
		{name: "budget", tool: ToolRemember, err: rememberapp.ErrRememberInputBudgetExceeded, code: string(domain.ErrorInvalidInput), reasonCode: "input_budget_exceeded", nextAction: actionContactOperator},
		{name: "stale", tool: ToolRemember, err: rememberapp.ErrRememberStaleInput, code: string(domain.ErrorConflict), reasonCode: "stale_state", nextAction: actionRefreshState},
		{name: "remember timeout", tool: ToolRemember, err: rememberapp.ErrRememberRequestTimeout, code: string(domain.ErrorProviderUnavailable), reasonCode: "request_timeout", nextAction: actionRetrySameRequest, retryable: true},
		{name: "remember cancelled", tool: ToolRemember, err: rememberapp.ErrRememberRequestCancelled, code: string(domain.ErrorConflict), reasonCode: "request_cancelled", nextAction: actionStop},
		{name: "idempotency", tool: ToolRemember, err: rememberapp.ErrRememberConflict, code: string(domain.ErrorConflict), reasonCode: "idempotency_conflict", nextAction: actionRefreshState},
		{name: "malformed", tool: ToolRemember, err: modelprovider.ErrVerifierMalformedResponse, code: string(domain.ErrorProviderMalformed), reasonCode: "provider_response_invalid", nextAction: actionRetrySameRequest, retryable: true},
		{name: "provider", tool: ToolRemember, err: modelprovider.ErrVerifierProvider, code: string(domain.ErrorProviderUnavailable), reasonCode: "provider_unavailable", nextAction: actionRetrySameRequest, retryable: true},
		{name: "rate limited", tool: ToolRemember, err: &modelprovider.RateLimitError{Provider: "test", Message: "secret", RetryAfter: 7}, code: string(domain.ErrorProviderUnavailable), reasonCode: "provider_unavailable", nextAction: actionRetrySameRequest, retryable: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := ActionableErrorData(ctx, test.tool, test.err)
			require.Equal(t, test.code, result["code"])
			require.Equal(t, test.reasonCode, result["reason_code"])
			require.Equal(t, test.nextAction, result["next_action"])
			require.Equal(t, test.retryable, result["retryable"])
			require.Equal(t, "actionable-correlation", result["correlation_id"])
			details, ok := result["details"].(map[string]any)
			require.True(t, ok)
			if test.name == "rate limited" {
				require.Equal(t, 7, details["retry_after_seconds"])
				require.Equal(t, 7, result["retry_after_seconds"])
			}
		})
	}
	longCorrelation := strings.Repeat("界", 129)
	bounded := ActionableErrorData(correlation.WithID(context.Background(), longCorrelation), ToolTraceMemory, repository.ErrTraceRelationshipNotFound)
	require.LessOrEqual(t, len([]rune(bounded["correlation_id"].(string))), 128)
	require.NotEqual(t, longCorrelation, bounded["correlation_id"])
	serverMessage := ActionableErrorData(ctx, ToolRecallMemory, httperr.New(httperr.SERVICE_UNAVAILABLE, "internal database password"))
	require.Equal(t, "service unavailable", serverMessage["message"])
	dreamInput := ActionableErrorData(ctx, ToolResolveDreamFeedback, fmt.Errorf("%w: hypothesis text cannot be submitted as its own evidence", dreamservice.ErrDreamFeedbackInvalidInput))
	require.Contains(t, dreamInput["message"], "evidence field")
	dreamDetails := dreamInput["details"].(map[string]any)
	require.Equal(t, "dream_feedback.evidence", dreamDetails["component"])
	require.Equal(t, true, dreamDetails["client_controlled"])
	inactiveExport := ActionableErrorData(ctx, ToolExportMemoryPack, fmt.Errorf("%w: relationship-1", skillpackservice.ErrMemoryPackRelationshipNotActive))
	require.Equal(t, string(domain.ErrorInvalidInput), inactiveExport["code"])
	require.Equal(t, "relationship_not_active", inactiveExport["reason_code"])
	require.Equal(t, actionRefreshState, inactiveExport["next_action"])
	require.False(t, inactiveExport["retryable"].(bool))
	inactiveDetails := inactiveExport["details"].(map[string]any)
	require.Equal(t, "memory_pack.relationship", inactiveDetails["component"])
	require.Equal(t, true, inactiveDetails["client_controlled"])
}

func TestActionableErrorDataMapsHTTPStatusesAndBudgetMeasurements(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "http-correlation")
	cases := []struct {
		name       string
		status     httperr.ErrorCode
		code       string
		reasonCode string
		nextAction string
		retryable  bool
	}{
		{name: "unauthorized", status: httperr.AUTH_INVALID, code: string(domain.ErrorUnauthorizedScope), reasonCode: "authorization_required", nextAction: actionAuthorization},
		{name: "not found", status: httperr.NOT_FOUND, code: string(domain.ErrorInvalidInput), reasonCode: "reference_not_found", nextAction: actionRefreshState},
		{name: "conflict", status: httperr.CONFLICT, code: string(domain.ErrorConflict), reasonCode: "state_conflict", nextAction: actionRefreshState},
		{name: "rate limited", status: httperr.RATE_LIMITED, code: string(domain.ErrorProviderUnavailable), reasonCode: "rate_limited", nextAction: actionRetrySameRequest, retryable: true},
		{name: "server", status: httperr.SERVICE_UNAVAILABLE, code: string(domain.ErrorProviderUnavailable), reasonCode: "service_unavailable", nextAction: actionRetrySameRequest, retryable: true},
		{name: "invalid", status: httperr.VALIDATION_ERROR, code: string(domain.ErrorInvalidInput), reasonCode: "invalid_request", nextAction: actionCorrectInput},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := ActionableErrorData(ctx, ToolRecallMemory, httperr.New(test.status, "safe public message"))
			require.Equal(t, test.code, result["code"])
			require.Equal(t, test.reasonCode, result["reason_code"])
			require.Equal(t, test.nextAction, result["next_action"])
			require.Equal(t, test.retryable, result["retryable"])
			wantMessage := "safe public message"
			if test.name == "server" {
				wantMessage = "service unavailable"
			}
			require.Equal(t, wantMessage, result["message"])
		})
	}

	measured := errors.Join(
		rememberapp.ErrRememberInputBudgetExceeded,
		&modelprovider.MalformedResponseError{
			FailureClass:    "input_budget",
			ValidationStage: "conversation_input_tokens",
			Measurement:     &modelprovider.FailureMeasurement{Unit: "tokens", Observed: 12, Limit: 10},
		},
	)
	result := ActionableErrorData(ctx, ToolRemember, measured)
	require.Equal(t, "assessor_conversation_input_exceeded", result["reason_code"])
	require.Equal(t, actionContactOperator, result["next_action"])
	details := result["details"].(map[string]any)
	require.Equal(t, "assessor.conversation", details["component"])
	require.Equal(t, 12, details["observed"])
	require.Equal(t, 10, details["limit"])
	require.Equal(t, true, details["server_owned"])
}

func TestActionableErrorHelpersExposeBoundedRecoveryActions(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "helper-correlation")
	invalid := ActionableInvalidInputData(ctx, ToolRemember, "bad_field", "bad field", "fix it")
	require.Equal(t, string(domain.ErrorInvalidInput), invalid["code"])
	require.Equal(t, "bad_field", invalid["reason_code"])
	require.Equal(t, actionCorrectInput, invalid["next_action"])
	require.Equal(t, false, invalid["retryable"])

	require.Equal(t, string(domain.ErrorUnauthorizedScope), ActionableAuthorizationData(ctx, ToolRemember)["code"])
	require.Equal(t, "tool_unavailable", ActionableToolUnavailableData(ctx, ToolRecallMemory)["reason_code"])
	require.Equal(t, "tool_result_serialization_failed", ActionableSerializationFailureData(ctx, ToolRecallMemory)["reason_code"])

	for _, tool := range []string{ToolRemember, ToolRetractEvidence, ToolCorrectRelationship, ToolSubmitRecallSessionFeedback} {
		require.True(t, actionableToolRequiresIdempotency(tool), tool)
		require.Contains(t, actionableTimeoutRemediation(tool), "same idempotency key")
		require.Contains(t, actionableTransientRemediation(tool), "same idempotency key")
	}
	require.False(t, actionableToolRequiresIdempotency(ToolResolveDreamFeedback))
	require.Contains(t, actionableTimeoutRemediation(ToolResolveDreamFeedback), "same arguments")
	require.Contains(t, actionableTransientRemediation(ToolResolveDreamFeedback), "same arguments")
	for _, tool := range []string{ToolRecallMemory, ToolTraceMemory, ToolListDreams, ToolGetDream, ToolExportMemoryPack} {
		require.True(t, actionableToolIsRead(tool), tool)
		require.Contains(t, actionableTimeoutRemediation(tool), "read request")
		require.Contains(t, actionableTransientRemediation(tool), "read request")
	}
	require.False(t, actionableToolRequiresIdempotency("other"))
	require.False(t, actionableToolIsRead("other"))
	require.Contains(t, actionableTimeoutRemediation("other"), "same arguments")
	require.Contains(t, actionableTransientRemediation("other"), "same arguments")

	require.False(t, actionableInputBudgetError(errors.New("ordinary error")))
	require.False(t, actionableInputBudgetError(&modelprovider.MalformedResponseError{FailureClass: "provider"}))
	require.True(t, actionableInputBudgetError(&modelprovider.MalformedResponseError{FailureClass: "input_budget"}))
}

func TestRememberToolResultErrorProjectsTerminalAndFailureResults(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "request-correlation")
	require.Nil(t, rememberToolResultError(ctx, nil))

	terminal := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: "terminal-submission",
		SubmissionKind: "remember", ProcessingState: "failed", SearchState: "not_required",
	}
	processErr := &rememberapp.RememberProcessError{
		Status: &rememberapp.SubmissionStatusResult{
			SubmissionID: "status-submission", CorrelationID: "durable-correlation",
		},
		Result: terminal,
		Err:    rememberapp.ErrRememberPersistence,
	}
	result := structuredToolResult(t, rememberToolResultError(ctx, processErr))
	require.Equal(t, "terminal-submission", result["submission_id"])
	require.Equal(t, "failed", result["processing_state"])

	security := structuredToolResult(t, rememberToolResultError(ctx, rememberapp.ErrEvidenceSecurityRejected))
	require.Equal(t, "failed", security["processing_state"])
	require.Equal(t, "request-correlation", security["correlation_id"])
	securityErrors := security["errors"].([]any)
	require.Equal(t, string(rememberapp.SubmissionErrorPolicyRejected), securityErrors[0].(map[string]any)["code"])

	statusFailure := &rememberapp.RememberProcessError{
		Status: &rememberapp.SubmissionStatusResult{
			SubmissionID: "status-submission", CorrelationID: "durable-correlation",
			ProcessingState: "failed",
			Errors:          []rememberapp.SubmissionStatusError{{Code: string(rememberapp.SubmissionErrorProviderUnavailable)}},
		},
		Err: errors.New("provider failed"),
	}
	failure := structuredToolResult(t, rememberToolResultError(ctx, statusFailure))
	require.Equal(t, "status-submission", failure["submission_id"])
	require.Equal(t, "durable-correlation", failure["correlation_id"])
	failureErrors := failure["errors"].([]any)
	require.Equal(t, string(rememberapp.SubmissionErrorProviderUnavailable), failureErrors[0].(map[string]any)["code"])

	statusRejection := &rememberapp.RememberProcessError{
		Status: &rememberapp.SubmissionStatusResult{
			SubmissionID: "rejected-submission", ProcessingState: "failed",
			Errors: []rememberapp.SubmissionStatusError{{Code: string(rememberapp.SubmissionErrorStaleInput)}},
		},
		Err: rememberapp.ErrRememberStaleInput,
	}
	rejection := structuredToolResult(t, rememberToolResultError(ctx, statusRejection))
	require.Equal(t, "failed", rejection["processing_state"])
	rejectionErrors := rejection["errors"].([]any)
	require.Equal(t, string(rememberapp.SubmissionErrorStaleInput), rejectionErrors[0].(map[string]any)["code"])
}

func TestRememberErrorCodeCoversSupportedFailureClasses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want rememberapp.SubmissionErrorCode
	}{
		{name: "conflict", err: memoryservice.ErrRememberConflict, want: rememberapp.SubmissionErrorIdempotencyConflict},
		{name: "stale", err: rememberapp.ErrRememberStaleInput, want: rememberapp.SubmissionErrorStaleInput},
		{name: "embedding unavailable", err: rememberapp.ErrRememberEmbeddingUnavailable, want: rememberapp.SubmissionErrorEmbeddingUnavailable},
		{name: "provider unavailable", err: rememberapp.ErrRememberProviderUnavailable, want: rememberapp.SubmissionErrorProviderUnavailable},
		{name: "provider response", err: rememberapp.ErrRememberProviderResponseInvalid, want: rememberapp.SubmissionErrorProviderResponseInvalid},
		{name: "input budget", err: rememberapp.ErrRememberInputBudgetExceeded, want: rememberapp.SubmissionErrorInputBudgetExceeded},
		{name: "embedding invalid", err: rememberapp.ErrRememberEmbeddingInvalid, want: rememberapp.SubmissionErrorEmbeddingResponseInvalid},
		{name: "commit conflict", err: rememberapp.ErrRememberCommitConflict, want: rememberapp.SubmissionErrorCommitConflict},
		{name: "timeout", err: rememberapp.ErrRememberRequestTimeout, want: rememberapp.SubmissionErrorRequestTimeout},
		{name: "cancelled", err: rememberapp.ErrRememberRequestCancelled, want: rememberapp.SubmissionErrorRequestCancelled},
		{name: "processor", err: rememberapp.ErrRememberProcessor, want: rememberapp.SubmissionErrorDatabaseFailure},
		{name: "persistence", err: rememberapp.ErrRememberPersistence, want: rememberapp.SubmissionErrorDatabaseFailure},
		{name: "unknown", err: errors.New("unknown"), want: rememberapp.SubmissionErrorInternalFailure},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, rememberErrorCode(test.err))
		})
	}
}

func TestCorrectionToolResultErrorMapsLifecycleAndHTTPFailures(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "correction-correlation")
	cases := []struct {
		name string
		err  error
		want rememberapp.SubmissionErrorCode
	}{
		{name: "embedding unavailable", err: memoryservice.ErrLifecycleEmbeddingUnavailable, want: rememberapp.SubmissionErrorEmbeddingUnavailable},
		{name: "embedding invalid", err: memoryservice.ErrLifecycleEmbeddingInvalid, want: rememberapp.SubmissionErrorEmbeddingResponseInvalid},
		{name: "embedding timeout", err: memoryservice.ErrLifecycleEmbeddingTimeout, want: rememberapp.SubmissionErrorRequestTimeout},
		{name: "request deadline", err: context.DeadlineExceeded, want: rememberapp.SubmissionErrorRequestTimeout},
		{name: "translated embedding unavailable", err: httperr.New(httperr.ErrEmbeddingUnavailable, "embedding provider unavailable"), want: rememberapp.SubmissionErrorEmbeddingUnavailable},
		{name: "translated embedding invalid", err: httperr.New(httperr.ErrEmbeddingResponseInvalid, "embedding provider response invalid"), want: rememberapp.SubmissionErrorEmbeddingResponseInvalid},
		{name: "translated embedding timeout", err: httperr.New(httperr.ErrEmbeddingTimeout, "embedding provider timed out"), want: rememberapp.SubmissionErrorRequestTimeout},
		{name: "cancelled", err: context.Canceled, want: rememberapp.SubmissionErrorRequestCancelled},
		{name: "not found", err: httperr.New(httperr.NOT_FOUND, "missing"), want: rememberapp.SubmissionErrorEntityNotFound},
		{name: "conflict", err: httperr.New(httperr.CONFLICT, "changed"), want: rememberapp.SubmissionErrorRelationshipChanged},
		{name: "database", err: errors.New("database failure"), want: rememberapp.SubmissionErrorDatabaseFailure},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := structuredToolResult(t, correctionToolResultError(ctx, " correction-submission ", test.err))
			require.Equal(t, "correction-submission", result["submission_id"])
			require.Equal(t, "correction-correlation", result["correlation_id"])
			resultErrors := result["errors"].([]any)
			require.Equal(t, string(test.want), resultErrors[0].(map[string]any)["code"])
		})
	}

	generated := structuredToolResult(t, correctionToolResultError(ctx, " ", errors.New("database failure")))
	require.NotEmpty(t, generated["submission_id"])
	entry := generated["errors"].([]any)[0].(map[string]any)
	canonicalDatabaseError := rememberapp.StatusError(rememberapp.SubmissionErrorDatabaseFailure)
	require.Equal(t, canonicalDatabaseError.NextAction, entry["next_action"])
	require.Equal(t, canonicalDatabaseError.Remediation, entry["remediation"])
}

func TestCorrectionToolResultErrorNormalizesOversizedCorrelationID(t *testing.T) {
	ctx := correlation.WithID(context.Background(), strings.Repeat("x", 129))
	result := structuredToolResult(t, correctionToolResultError(ctx, "correction-submission", errors.New("database failure")))

	correlationID, ok := result["correlation_id"].(string)
	require.True(t, ok)
	require.NotEqual(t, strings.Repeat("x", 129), correlationID)
	require.LessOrEqual(t, utf8.RuneCountInString(correlationID), 128)
	_, parseErr := uuid.Parse(correlationID)
	require.NoError(t, parseErr)
}

func TestCorrectionToolResultErrorPreservesTypedConflictReasons(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "correction-correlation")
	cases := []struct {
		name           string
		reason         string
		wantCode       rememberapp.SubmissionErrorCode
		wantState      string
		wantNextAction string
		wantClient     bool
	}{
		{
			name: "entity not found", reason: "not found",
			wantCode: rememberapp.SubmissionErrorEntityNotFound, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection), wantClient: true,
		},
		{
			name: "idempotency", reason: string(rememberapp.SubmissionErrorIdempotencyConflict),
			wantCode: rememberapp.SubmissionErrorIdempotencyConflict, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection), wantClient: true,
		},
		{
			name: "confirmation expired", reason: string(rememberapp.SubmissionErrorConfirmationExpired),
			wantCode: rememberapp.SubmissionErrorConfirmationExpired, wantState: "rejected",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection), wantClient: true,
		},
		{
			name: "commit fence", reason: string(rememberapp.SubmissionErrorCommitConflict),
			wantCode: rememberapp.SubmissionErrorCommitConflict, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetrySameRequest), wantClient: false,
		},
		{
			name: "invalid confirmation", reason: memoryservice.CorrectionConfirmationInvalidReason,
			wantCode: rememberapp.SubmissionErrorRelationshipChanged, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection), wantClient: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.name == "entity not found" {
				err = httperr.New(httperr.NOT_FOUND, "submission not found")
			} else {
				err = httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{Field: "reason", Message: test.reason}})
			}
			result := structuredToolResult(t, correctionToolResultError(ctx, "correction-submission", err))
			require.Equal(t, test.wantCode, rememberapp.SubmissionErrorCode(result["errors"].([]any)[0].(map[string]any)["code"].(string)))
			require.Equal(t, test.wantState, result["processing_state"])
			require.Equal(t, test.wantNextAction, result["errors"].([]any)[0].(map[string]any)["next_action"])
			details := result["errors"].([]any)[0].(map[string]any)["details"].(map[string]any)
			if test.wantClient {
				require.Equal(t, true, details["client_controlled"])
				require.NotContains(t, details, "server_owned")
			} else {
				require.Equal(t, true, details["server_owned"])
				require.NotContains(t, details, "client_controlled")
			}
			if test.wantCode == rememberapp.SubmissionErrorIdempotencyConflict {
				require.Equal(t, "Retry correct_relationship with current relationship state and a new idempotency_key.", result["errors"].([]any)[0].(map[string]any)["remediation"])
			}
		})
	}
}

func structuredToolResult(t *testing.T, err error) map[string]any {
	t.Helper()
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	return structured.Result
}
