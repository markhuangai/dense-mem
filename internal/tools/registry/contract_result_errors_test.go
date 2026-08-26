package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberToolResultErrorUsesClosedOperationalCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"idempotency", rememberapp.ErrRememberConflict, "idempotency_conflict"},
		{"legacy idempotency", memoryservice.ErrRememberConflict, "idempotency_conflict"},
		{"stale input", rememberapp.ErrRememberStaleInput, "stale_input"},
		{"provider unavailable", rememberapp.ErrRememberProviderUnavailable, "provider_unavailable"},
		{"provider response invalid", rememberapp.ErrRememberProviderResponseInvalid, "provider_response_invalid"},
		{"input budget", rememberapp.ErrRememberInputBudgetExceeded, "input_budget_exceeded"},
		{"embedding unavailable", rememberapp.ErrRememberEmbeddingUnavailable, "embedding_unavailable"},
		{"embedding invalid", rememberapp.ErrRememberEmbeddingInvalid, "embedding_response_invalid"},
		{"commit conflict", rememberapp.ErrRememberCommitConflict, "commit_conflict"},
		{"timeout", rememberapp.ErrRememberRequestTimeout, "request_timeout"},
		{"cancelled", rememberapp.ErrRememberRequestCancelled, "request_cancelled"},
		{"database", rememberapp.ErrRememberPersistence, "database_failure"},
		{"processor", rememberapp.ErrRememberProcessor, "database_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			structured, ok := ToolResultFromError(rememberToolResultError(context.Background(), test.err))
			require.True(t, ok)
			items, ok := structured.Result["errors"].([]any)
			require.True(t, ok)
			require.Len(t, items, 1)
			item, ok := items[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, test.code, item["code"])
			require.NotEmpty(t, item["next_action"])
			require.NotEmpty(t, item["remediation"])
		})
	}
}

func TestRememberToolResultErrorNilIsNotStructured(t *testing.T) {
	_, ok := ToolResultFromError(rememberToolResultError(context.Background(), nil))
	require.False(t, ok)
}

func TestRememberToolResultErrorQuarantinesSecurityRejection(t *testing.T) {
	structured, ok := ToolResultFromError(rememberToolResultError(context.Background(), rememberapp.ErrEvidenceSecurityRejected))
	require.True(t, ok)
	items := structured.Result["errors"].([]any)
	item := items[0].(map[string]any)
	require.Equal(t, "submission_quarantined", item["code"])
	require.Equal(t, "quarantined", structured.Result["processing_state"])
	_, present := structured.Result["degradations"]
	require.False(t, present)
}

func TestRememberToolResultErrorFallsBackToInternalFailure(t *testing.T) {
	structured, ok := ToolResultFromError(rememberToolResultError(context.Background(), errors.New("opaque")))
	require.True(t, ok)
	item := structured.Result["errors"].([]any)[0].(map[string]any)
	require.Equal(t, "internal_failure", item["code"])
}

func TestRememberToolResultErrorUsesDurableFailureStatus(t *testing.T) {
	status := &rememberapp.SubmissionStatusResult{
		SubmissionID:    "durable-submission",
		ProcessingState: "failed",
		Errors:          []rememberapp.SubmissionStatusError{rememberapp.StatusError(rememberapp.SubmissionErrorEmbeddingUnavailable)},
	}
	structured, ok := ToolResultFromError(rememberToolResultError(context.Background(), &rememberapp.RememberProcessError{
		Status: status, Err: rememberapp.ErrRememberPersistence,
	}))
	require.True(t, ok)
	require.Equal(t, "durable-submission", structured.Result["submission_id"])
	item := structured.Result["errors"].([]any)[0].(map[string]any)
	require.Equal(t, "embedding_unavailable", item["code"])
}

func TestCorrectionToolResultErrorIsBoundedAndRetryable(t *testing.T) {
	structured, ok := ToolResultFromError(correctionToolResultError(context.Background(), errors.New("raw correction failure")))
	require.True(t, ok)
	require.Equal(t, "relationship_correction", structured.Result["submission_kind"])
	require.NotEmpty(t, structured.Result["submission_id"])
	item := structured.Result["errors"].([]any)[0].(map[string]any)
	require.Equal(t, "database_failure", item["code"])
	require.Equal(t, true, item["retryable"])
	require.Equal(t, "retry_correction", item["next_action"])
	require.NotContains(t, item["remediation"], "raw correction failure")
}

func TestCorrectionToolResultErrorPreservesEmbeddingFailureCodes(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{memoryservice.ErrLifecycleEmbeddingUnavailable, "embedding_unavailable"},
		{memoryservice.ErrLifecycleEmbeddingInvalid, "embedding_response_invalid"},
		{memoryservice.ErrLifecycleEmbeddingTimeout, "request_timeout"},
		{memoryservice.ErrLifecycleEmbeddingCancelled, "request_cancelled"},
	} {
		structured, ok := ToolResultFromError(correctionToolResultError(context.Background(), test.err))
		require.True(t, ok)
		item := structured.Result["errors"].([]any)[0].(map[string]any)
		require.Equal(t, test.code, item["code"])
	}
}
