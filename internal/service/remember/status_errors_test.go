package remember

import (
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
