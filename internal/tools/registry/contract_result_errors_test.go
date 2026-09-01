package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberToolResultErrorProjectsTerminalAndFailureResults(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "request-correlation")
	require.Nil(t, rememberToolResultError(ctx, nil))

	terminal := &rememberapp.TerminalRememberResult{
		ContractVersion: "dense-mem.v2.6.1", SubmissionID: "terminal-submission",
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
	require.Equal(t, "quarantined", security["processing_state"])
	require.Equal(t, "request-correlation", security["correlation_id"])
	securityErrors := security["errors"].([]any)
	require.Equal(t, string(rememberapp.SubmissionErrorQuarantined), securityErrors[0].(map[string]any)["code"])

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
			SubmissionID: "rejected-submission", ProcessingState: "rejected",
			Errors: []rememberapp.SubmissionStatusError{{Code: string(rememberapp.SubmissionErrorStaleInput)}},
		},
		Err: rememberapp.ErrRememberStaleInput,
	}
	rejection := structuredToolResult(t, rememberToolResultError(ctx, statusRejection))
	require.Equal(t, "rejected", rejection["processing_state"])
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
	}{
		{
			name: "idempotency", reason: string(rememberapp.SubmissionErrorIdempotencyConflict),
			wantCode: rememberapp.SubmissionErrorIdempotencyConflict, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection),
		},
		{
			name: "confirmation expired", reason: string(rememberapp.SubmissionErrorConfirmationExpired),
			wantCode: rememberapp.SubmissionErrorConfirmationExpired, wantState: "rejected",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection),
		},
		{
			name: "commit fence", reason: string(rememberapp.SubmissionErrorCommitConflict),
			wantCode: rememberapp.SubmissionErrorCommitConflict, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetrySameRequest),
		},
		{
			name: "invalid confirmation", reason: memoryservice.CorrectionConfirmationInvalidReason,
			wantCode: rememberapp.SubmissionErrorRelationshipChanged, wantState: "failed",
			wantNextAction: string(rememberapp.SubmissionNextActionRetryCorrection),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{Field: "reason", Message: test.reason}})
			result := structuredToolResult(t, correctionToolResultError(ctx, "correction-submission", err))
			require.Equal(t, test.wantCode, rememberapp.SubmissionErrorCode(result["errors"].([]any)[0].(map[string]any)["code"].(string)))
			require.Equal(t, test.wantState, result["processing_state"])
			require.Equal(t, test.wantNextAction, result["errors"].([]any)[0].(map[string]any)["next_action"])
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
