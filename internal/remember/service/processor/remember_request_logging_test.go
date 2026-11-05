package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

type requestPayloadSink struct{ records []observability.LogRecord }

func (s *requestPayloadSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	s.records = append(s.records, record)
	return nil
}

func TestRememberRequestLoggingStartsAfterInitialSecurityAdmission(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		sink := &requestPayloadSink{}
		protector := observability.NewCredentialProtector("actual-key")
		var output bytes.Buffer
		logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		require.NoError(t, logger.AttachSink(sink))
		processor := &rememberSynchronousProcessor{logger: logger, protector: protector}
		processor.logAdmittedRememberRequest(context.Background(), remember.RememberProcessRequest{InitialSecurityRejected: rejected, OriginalRequest: []byte(`{"secret":"admitted fact","api_key":"actual-key"}`)})
		if rejected {
			require.Empty(t, sink.records)
			require.Empty(t, output.String())
			continue
		}
		require.Empty(t, sink.records, "successful request payloads must not enter the portal log store")
		var record map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &record))
		encoded, err := json.Marshal(record)
		require.NoError(t, err)
		require.Contains(t, string(encoded), "admitted fact")
		require.NotContains(t, string(encoded), "actual-key")
		require.NotContains(t, string(encoded), "diagnostic_unavailable_reason")
	}
}

func TestRememberInvocationSecurityMetadataIsBoundedAndContentFree(t *testing.T) {
	for _, classification := range []string{"execution", "replay"} {
		t.Run(classification, func(t *testing.T) {
			const content = "rejected-content-canary"
			issues := make([]any, 40)
			for index := range issues {
				issues[index] = map[string]any{"path": "/evidence/0/content", "code": "encoded_payload", "span_start": 0, "span_end": 64, "message": content, "content": content}
			}
			status := &remember.SubmissionStatusResult{ProcessingState: "failed", Errors: []remember.SubmissionStatusError{{
				Code: "submission_policy_rejected", ReasonCode: "security_rejection", Details: map[string]any{
					"component": "remember.initial_scan", "client_controlled": true, "issues": issues, "raw_content": content,
				},
			}}}
			if classification == "replay" {
				stored, err := json.Marshal(status)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(stored, &status))
			}
			var output bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			processor := &rememberSynchronousProcessor{ledger: &rememberFailureLedgerStub{}, logger: logger}
			processor.recordRememberInvocation(context.Background(), remember.RememberProcessRequest{
				SecurityRejected: true, InitialSecurityRejected: true, OriginalRequest: []byte(content),
			}, uuid.NewString(), classification, uuid.NewString(), "assessment", remember.ErrRememberPolicyRejected, status, nil)
			require.NotContains(t, output.String(), content)
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Equal(t, "ERROR", record["level"])
			require.Equal(t, "remember_invocation_completed", record["msg"])
			require.Equal(t, classification, record["classification"])
			require.Equal(t, "security_rejection", record["reason_code"])
			security := record["security_diagnostics"].(map[string]any)
			require.Equal(t, "remember.initial_scan", security["component"])
			require.Equal(t, true, security["issues_truncated"])
			bounded := security["issues"].([]any)
			require.Len(t, bounded, 20)
			issue := bounded[0].(map[string]any)
			require.Equal(t, "encoded_payload", issue["code"])
			require.Equal(t, float64(0), issue["span_start"])
			require.Equal(t, float64(64), issue["span_end"])
		})
	}
}
