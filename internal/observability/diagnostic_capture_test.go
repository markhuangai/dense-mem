package observability

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialProtectorProtectDiagnosticBytesPreservesBoundedCaptureStates(t *testing.T) {
	protector := NewCredentialProtector("configured-secret")

	body, reason := protector.ProtectDiagnosticBytes(nil, 128)
	require.Empty(t, body)
	require.Equal(t, CredentialProtectionAvailable, reason)

	body, reason = protector.ProtectDiagnosticBytes([]byte(`{"message":"configured-secret"}`), 128)
	require.Equal(t, CredentialProtectionAvailable, reason)
	require.Contains(t, string(body), CredentialProtectionRedacted)
	require.NotContains(t, string(body), "configured-secret")

	body, reason = protector.ProtectDiagnosticBytes([]byte("provider failed: configured-secret"), 128)
	require.Equal(t, CredentialProtectionAvailable, reason)
	require.Contains(t, string(body), CredentialProtectionRedacted)

	body, reason = protector.ProtectDiagnosticBytes([]byte(`{"message":"too large"}`), 2)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionBudgetExceeded, reason)

	body, reason = protector.ProtectDiagnosticBytes([]byte(strings.Repeat(" ", 129)+`{}`), 128)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionBudgetExceeded, reason)

	body, reason = protector.ProtectDiagnosticBytes([]byte(`{}`), 0)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionInvalidBudget, reason)
}

func TestCredentialProtectorProtectDiagnosticBytesReportsUnavailableProtection(t *testing.T) {
	protector := NewCredentialProtector(strings.Repeat("x", MaxCredentialSecretBytes+1))
	body, reason := protector.ProtectDiagnosticBytes([]byte(`{"message":"admitted"}`), 128)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionSecretTooLong, reason)
}

func TestCredentialProtectorProtectDiagnosticBytesPreservesNumbersAndRejectsTrailingValues(t *testing.T) {
	protector := NewCredentialProtector()
	body, reason := protector.ProtectDiagnosticBytes([]byte(`{"value":9007199254740993}`), 128)
	require.Equal(t, CredentialProtectionAvailable, reason)
	require.Equal(t, `{"value":9007199254740993}`, string(body))

	body, reason = protector.ProtectDiagnosticBytes([]byte(`{"value":1} {"value":2}`), 128)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionFormattingFailed, reason)
}

func TestCredentialProtectorProtectDiagnosticBytesReportsUnavailableInvalidJSONProtection(t *testing.T) {
	protector := NewCredentialProtector(strings.Repeat("x", MaxCredentialSecretBytes+1))
	body, reason := protector.ProtectDiagnosticBytes([]byte("not-json"), 128)
	require.Nil(t, body)
	require.Equal(t, CredentialProtectionSecretTooLong, reason)
}
