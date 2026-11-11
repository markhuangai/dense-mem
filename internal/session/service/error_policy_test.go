package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyUnitSessionFailuresPreserveRetryPolicy(t *testing.T) {
	for _, test := range []struct {
		cause        error
		code, action string
		retryable    bool
	}{
		{context.Canceled, "request_cancelled", "retry_same_request", true},
		{context.DeadlineExceeded, "request_timeout", "retry_same_request", true},
		{modelprovider.ErrVerifierTimeout, "provider_unavailable", "retry_same_request", true},
		{modelprovider.ErrVerifierMalformedResponse, "provider_response_invalid", "retry_same_request", true},
		{session.ErrBudget, "input_budget_exceeded", "contact_operator", false},
		{session.ErrSecurity, "submission_policy_rejected", "none", false},
		{remember.ErrEvidenceSecurityRejected, "submission_policy_rejected", "none", false},
		{remember.ErrRememberPolicyRejected, "submission_policy_rejected", "none", false},
		{session.ErrStale, "stale_input", "contact_operator", false},
		{&session.ProcessingError{Code: "input_budget_exceeded"}, "input_budget_exceeded", "contact_operator", false},
		{errors.New("private database detail"), "database_failure", "retry_same_request", true},
	} {
		t.Run(test.code+"/"+fmt.Sprintf("%T", test.cause), func(t *testing.T) {
			result := classifySessionError(fmt.Errorf("private wrapper: %w", test.cause))
			require.Equal(t, test.code, result.Code)
			require.Equal(t, test.action, result.NextAction)
			require.Equal(t, test.retryable, result.Retryable)
			require.NotEmpty(t, result.Remediation)
			require.NotContains(t, result.Message, "private")
		})
	}
}
