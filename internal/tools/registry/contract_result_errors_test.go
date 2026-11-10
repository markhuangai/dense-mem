package registry

import (
	"context"
	"errors"

	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"

	"github.com/markhuangai/dense-mem/internal/httperr"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
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
		{name: "reference", tool: ToolTraceMemory, err: tracecontract.ErrEvidenceNotFound, code: string(domain.ErrorInvalidInput), reasonCode: "reference_not_found", nextAction: actionRefreshState},
		{name: "invalid trace id", tool: ToolTraceMemory, err: tracecontract.ErrEvidenceIDInvalid, code: string(domain.ErrorInvalidInput), reasonCode: "invalid_request", nextAction: actionCorrectInput},
		{name: "invalid retract evidence id", tool: ToolRetractEvidence, err: knowledgecontract.ErrEvidenceLifecycleIDInvalid, code: string(domain.ErrorInvalidInput), reasonCode: "invalid_request", nextAction: actionCorrectInput},
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

			if test.name == "invalid retract evidence id" {
				require.Equal(t, "retract_evidence.evidence_ids", details["component"])
				require.Equal(t, true, details["client_controlled"])
			}
		})
	}
	longCorrelation := strings.Repeat("界", 129)
	bounded := ActionableErrorData(correlation.WithID(context.Background(), longCorrelation), ToolTraceMemory, tracecontract.ErrEvidenceNotFound)
	require.LessOrEqual(t, len([]rune(bounded["correlation_id"].(string))), 128)
	require.NotEqual(t, longCorrelation, bounded["correlation_id"])
	serverMessage := ActionableErrorData(ctx, ToolRecallMemory, httperr.New(httperr.SERVICE_UNAVAILABLE, "internal database password"))
	require.Equal(t, "service unavailable", serverMessage["message"])

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
	invalidTrace := ActionableErrorData(ctx, ToolTraceMemory, tracecontract.ErrEvidenceIDInvalid)
	require.Equal(t, "trace.evidence_id", invalidTrace["details"].(map[string]any)["component"])
	require.Equal(t, true, invalidTrace["details"].(map[string]any)["client_controlled"])

	guided := ActionableErrorData(ctx, ToolRemember, httperr.WithGuidance(
		httperr.New(httperr.CONFLICT, "team name already exists"),
		"team_name_conflict", actionCorrectInput, "Choose a different team name and submit again.", false, nil, "",
	))
	require.Equal(t, string(domain.ErrorConflict), guided["code"])
	require.Equal(t, "team_name_conflict", guided["reason_code"])
	require.Equal(t, actionCorrectInput, guided["next_action"])
	require.Equal(t, "Choose a different team name and submit again.", guided["remediation"])
	require.False(t, guided["retryable"].(bool))
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

	for _, tool := range []string{ToolRemember, ToolRetractEvidence, ToolSubmitRecallSessionFeedback} {
		require.True(t, actionableToolRequiresIdempotency(tool), tool)
		require.Contains(t, actionableTimeoutRemediation(tool), "same idempotency key")
		require.Contains(t, actionableTransientRemediation(tool), "same idempotency key")
	}
	for _, tool := range []string{ToolRecallMemory, ToolTraceMemory, ToolExportMemoryPack} {
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
	require.Nil(t, rememberToolResultError(ctx, domain.MemorySpacePrivate, nil))

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
	result := structuredToolResult(t, rememberToolResultError(ctx, domain.MemorySpacePrivate, processErr))
	require.Equal(t, "terminal-submission", result["submission_id"])
	require.Equal(t, "failed", result["processing_state"])

	security := structuredToolResult(t, rememberToolResultError(ctx, domain.MemorySpacePrivate, rememberapp.ErrEvidenceSecurityRejected))
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
	failure := structuredToolResult(t, rememberToolResultError(ctx, domain.MemorySpacePrivate, statusFailure))
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
	rejection := structuredToolResult(t, rememberToolResultError(ctx, domain.MemorySpacePrivate, statusRejection))
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
		{name: "conflict", err: rememberapp.ErrRememberConflict, want: rememberapp.SubmissionErrorIdempotencyConflict},
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

func structuredToolResult(t *testing.T, err error) map[string]any {
	t.Helper()
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	return structured.Result
}
