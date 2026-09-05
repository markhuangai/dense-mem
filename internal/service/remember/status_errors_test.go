package remember

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmissionStatusErrorsExposeClosedGuidance(t *testing.T) {
	codes := SubmissionErrorCodes()
	require.NotEmpty(t, codes)
	require.Equal(t, []string{
		string(SubmissionNextActionRetrySameRequest),
		string(SubmissionNextActionResubmitRemember),
		string(SubmissionNextActionRetryCorrection),
		string(SubmissionNextActionContactOperator),
		string(SubmissionNextActionNone),
	}, SubmissionNextActions())

	for _, raw := range codes {
		code := SubmissionErrorCode(raw)
		status := StatusError(code)
		require.Equal(t, raw, status.Code)
		require.NotEmpty(t, status.Message)
		require.NotEmpty(t, status.NextAction)
		require.NotEmpty(t, status.Remediation)
	}

	require.Equal(t, string(SubmissionErrorInternalFailure), StatusError("provider-secret-detail").Code)
	require.Equal(t, "safe override", StatusErrorWithMessage(SubmissionErrorInternalFailure, "safe override").Message)
	serverOwnedBudget := StatusErrorWithDetails(SubmissionErrorInputBudgetExceeded, "entity_catalog", map[string]any{"server_owned": true})
	require.Equal(t, SubmissionNextActionContactOperator, SubmissionNextAction(serverOwnedBudget.NextAction))
	require.Equal(t, serverOwnedInputBudgetRemediation, serverOwnedBudget.Remediation)
	require.Equal(t, string(SubmissionErrorPolicyRejected), StatusErrorForCode("unknown", "rejected").Code)
	require.Equal(t, string(SubmissionErrorInternalFailure), StatusErrorForCode("unknown", "failed").Code)

	for _, test := range []struct {
		stage string
		class string
		want  SubmissionErrorCode
	}{
		{stage: "contract_superseded", want: SubmissionErrorInternalFailure},
		{stage: "input_budget", want: SubmissionErrorInputBudgetExceeded},
		{stage: "entity_catalog", want: SubmissionErrorInputBudgetExceeded},
		{stage: "configuration_invalid", want: SubmissionErrorConfigurationInvalid},
		{stage: "database", want: SubmissionErrorDatabaseFailure},
		{stage: "assessment", class: "malformed_exhausted", want: SubmissionErrorProviderResponseInvalid},
		{class: "request_invalid", want: SubmissionErrorProviderResponseInvalid},
		{class: "provider_unavailable", want: SubmissionErrorProviderUnavailable},
		{want: SubmissionErrorInternalFailure},
	} {
		require.Equal(t, test.want, FailureCode(test.stage, test.class))
	}
}

func TestStatusErrorWithDetailsBoundsSafeValuesAndServerGuidance(t *testing.T) {
	details := map[string]any{
		"server_owned": true,
		"string":       strings.Repeat("s", 600),
		"int":          1,
		"int64":        int64(2),
		"bool":         true,
		"float":        float64(3),
		"ignored":      []string{"unsupported"},
		"":             "empty key",
	}
	result := StatusErrorWithDetails(SubmissionErrorInputBudgetExceeded, strings.Repeat("r", 200), details)
	require.Equal(t, 1, result.Details["int"])
	require.Equal(t, int64(2), result.Details["int64"])
	require.Equal(t, true, result.Details["bool"])
	require.Equal(t, float64(3), result.Details["float"])
	require.LessOrEqual(t, len(result.Details["string"].(string)), 512)
	require.Equal(t, SubmissionNextActionContactOperator, SubmissionNextAction(result.NextAction))
	require.Equal(t, serverOwnedInputBudgetRemediation, result.Remediation)

	for index := 0; index < 25; index++ {
		details["extra_"+string(rune('a'+index))] = index
	}
	result = StatusErrorWithDetails(SubmissionErrorInputBudgetExceeded, strings.Repeat("r", 200), details)
	require.LessOrEqual(t, len(result.Details), 20)
	require.LessOrEqual(t, len(result.ReasonCode), 128)

	withoutOwnership := StatusErrorWithDetails(SubmissionErrorInputBudgetExceeded, "reason", map[string]any{"server_owned": false})
	require.NotEqual(t, SubmissionNextActionContactOperator, SubmissionNextAction(withoutOwnership.NextAction))
	withoutDetails := StatusErrorWithDetails(SubmissionErrorInputBudgetExceeded, "reason", nil)
	require.Nil(t, withoutDetails.Details)
}
