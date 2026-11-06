package observability

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialProtectionBudgetCausesRetainLegacyCodes(t *testing.T) {
	require.Equal(t, 2, int(CredentialProtectionBudgetExceeded))
	require.Equal(t, "credential_protection_2", CredentialProtectionBudgetExceeded.CaptureReason(128))
	require.Equal(t, "budget_exceeded_unspecified", CredentialProtectionBudgetExceeded.Code())
	for _, test := range []struct {
		name   string
		value  any
		limit  int
		reason CredentialProtectionUnavailableReason
	}{
		{"processing", strings.Repeat("a", 128), 32, CredentialProtectionProcessingBudgetExceeded},
		{"encoding", "\n", 3, CredentialProtectionEncodedBudgetExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := NewCredentialProtector("unrelated").Snapshot(test.value, test.limit)
			require.Nil(t, result.Value)
			require.Equal(t, test.reason, result.UnavailableReason)
		})
	}
	chunks := CaptureDiagnosticBody(context.Background(), NewCredentialProtector(), []byte("too large"), 2)
	require.Equal(t, "body_bytes_exceeded", chunks[0].ReasonCode)
	require.Equal(t, 2, chunks[0].Limit)
	require.Equal(t, "body_bytes", chunks[0].LimitUnit)
	chunks = CaptureDiagnosticBody(context.Background(), NewCredentialProtector(), []byte{0xff}, 100)
	require.Equal(t, "invalid_encoding", chunks[0].ReasonCode)
	require.Empty(t, chunks[0].Body)
	for reason := CredentialProtectionAvailable; reason <= CredentialProtectionBodyBudgetExceeded; reason++ {
		require.NotEqual(t, "unknown", reason.Code())
	}
	require.Equal(t, "unknown", CredentialProtectionUnavailableReason(255).Code())
	require.Contains(t, CredentialProtectionMatchingBudgetExceeded.CaptureReason(16<<20), "matching_work=8388608")
}
