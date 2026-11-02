package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDebugPayloadsStayOutOfPortalOperationLogs(t *testing.T) {
	var console bytes.Buffer
	logger := NewWithHandler(slog.NewJSONHandler(&console, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := &recordingLogSink{}
	require.NoError(t, logger.AttachSink(sink))
	LogDiagnosticBody(context.Background(), logger.ContextSlog(), NewCredentialProtector(), "ai_exchange_payload", []byte(`{"messages":"successful body canary"}`), 1<<20)
	logger.InfoContext(context.Background(), "operation_completed", String("outcome", "completed"))
	require.Contains(t, console.String(), "successful body canary")
	require.Len(t, sink.records, 1)
	require.Equal(t, "operation_completed", sink.records[0].Message)
	encoded, err := json.Marshal(sink.records)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "successful body canary")
}

func TestDiagnosticChunksProtectBeforeSplittingAndPreserveWireText(t *testing.T) {
	secret := "actual-configured-key"
	body := strings.Repeat("x", 2040) + secret + ` {"secret":"ordinary content"} {not json}` + strings.Repeat("\u0001", 3000)
	chunks := CaptureDiagnosticBody(context.Background(), NewCredentialProtector(secret), []byte(body), 1<<20)
	var got strings.Builder
	for index, chunk := range chunks {
		require.Equal(t, index, chunk.Index)
		require.Equal(t, len(chunks), chunk.Count)
		require.Equal(t, "captured", chunk.State)
		encoded, err := json.Marshal(map[string]any{"payload_chunk": chunk, "team_id": strings.Repeat("a", 36), "run_id": strings.Repeat("b", 36)})
		require.NoError(t, err)
		require.Less(t, len(encoded), MaxOperationMetadataBytes)
		require.NotContains(t, string(encoded), secret)
		got.WriteString(chunk.Body)
	}
	require.Equal(t, strings.ReplaceAll(body, secret, CredentialProtectionRedacted), got.String())
	digest := sha256.Sum256([]byte(got.String()))
	require.Equal(t, hex.EncodeToString(digest[:]), chunks[0].SHA256)
}

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
