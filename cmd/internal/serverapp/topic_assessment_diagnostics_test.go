package serverapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologySemanticAssessmentRealLogger(t *testing.T) {
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			const secret = "assessment-operational-credential"
			team, operation, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
			ctx := observability.WithExchangeScope(context.Background(), observability.ExchangeScope{TeamID: team, OperationID: operation, RunID: "run", BatchID: "batch"})
			sink := &ontologyDiagnosticTestSink{}
			console, records := captureOntologyPublicationConsole(t, func() {
				protector := observability.NewCredentialProtector(secret)
				root := observability.NewWithProtector(level, protector)
				require.NoError(t, root.AttachSink(sink))
				observe := topics.MaintenanceDiagnostics(nil, protector, root.ContextSlog()).Observe
				request := assessment.Request{RequestID: id, Items: []assessment.Item{{Ref: "s0", PreferredDefinitionRef: "d0", Context: map[string]string{
					"token": "exact admitted token", "password": "exact admitted password", "secret": "exact admitted secret",
					"content": strings.Repeat("admitted source context ", 250) + secret}}}}
				observe(ctx, assessment.Diagnostic{Stage: "prepared", TeamID: team, Request: &request, Model: "test-model"})
				observe(ctx, assessment.Diagnostic{Stage: "rejected", Request: &request, Attempt: 1, Error: errors.New("invalid hierarchy " + secret)})
				observe(ctx, assessment.Diagnostic{Stage: "completed", Request: &request, Result: &contract.OrganizationResult{AssessmentID: id, Current: true}})
			})
			require.NotContains(t, console, secret)
			require.Contains(t, console, "topic_semantic_assessment_rejected")
			require.Contains(t, console, "topic_semantic_assessment_completed")
			require.NotContains(t, console, "topic_semantic_assessment_failed")
			var body []byte
			count := 0
			for _, record := range records {
				require.Equal(t, team, record["team_id"])
				require.Equal(t, operation, record["operation_id"])
				require.Equal(t, id, record["assessment_id"])
				if record["msg"] != "topic_semantic_assessment" || record["stage"] != "prepared" || record["payload_chunk"] == nil {
					continue
				}
				encoded, err := json.Marshal(record["payload_chunk"])
				require.NoError(t, err)
				var chunk observability.DiagnosticBodyChunk
				require.NoError(t, json.Unmarshal(encoded, &chunk))
				require.Equal(t, "captured", chunk.State)
				require.Equal(t, count, chunk.Index)
				require.LessOrEqual(t, len([]byte(chunk.Body)), 2048)
				body = append(body, []byte(chunk.Body)...)
				count++
				if count == chunk.Count {
					digest := sha256.Sum256(body)
					require.Equal(t, hex.EncodeToString(digest[:]), chunk.SHA256)
				}
			}
			if level <= slog.LevelDebug {
				require.Greater(t, count, 1)
				var payload struct {
					Request assessment.Request `json:"request"`
				}
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Equal(t, "exact admitted token", payload.Request.Items[0].Context["token"])
				require.Equal(t, "exact admitted password", payload.Request.Items[0].Context["password"])
				require.Equal(t, "exact admitted secret", payload.Request.Items[0].Context["secret"])
				require.Contains(t, payload.Request.Items[0].Context["content"], observability.CredentialProtectionRedacted)
			} else {
				require.Zero(t, count)
				require.NotContains(t, console, "exact admitted")
			}
			for _, record := range sink.records {
				require.NotContains(t, record.Attrs, "payload_chunk")
			}
		})
	}
}

func TestOntologySemanticAssessmentIncompleteAndFailed(t *testing.T) {
	console, _ := captureOntologyPublicationConsole(t, func() {
		protector := observability.NewCredentialProtector()
		root := observability.NewWithProtector(slog.LevelDebug, protector)
		observe := topics.MaintenanceDiagnostics(nil, protector, root.ContextSlog()).Observe
		request := assessment.Request{RequestID: uuid.NewString(), Items: []assessment.Item{{Text: strings.Repeat("x", 16*1024*1024+1)}}}
		observe(context.Background(), assessment.Diagnostic{Stage: "completed", Request: &request,
			Result: &contract.OrganizationResult{AssessmentID: request.RequestID, FailureCode: "stale_input"}, Error: contract.ErrSourceStale})
	})
	require.Contains(t, console, "topic_semantic_assessment_capture_incomplete")
	require.Contains(t, console, "body_bytes_exceeded")
	require.Contains(t, console, "topic_semantic_assessment_failed")
	require.NotContains(t, console, "topic_semantic_assessment_completed")
	require.NotContains(t, console, strings.Repeat("x", 100))
}

func TestOntologySemanticAssessmentDefinitionVersionRejection(t *testing.T) {
	id, recordID := uuid.NewString(), uuid.NewString()
	rejection := contract.FreshnessRejection{Check: "dependency_version", RecordID: recordID, Expected: "1", Observed: "2"}
	console, records := captureOntologyPublicationConsole(t, func() {
		protector := observability.NewCredentialProtector()
		root := observability.NewWithProtector(slog.LevelDebug, protector)
		topics.MaintenanceDiagnostics(nil, protector, root.ContextSlog()).Observe(context.Background(), assessment.Diagnostic{Stage: "completed",
			Result: &contract.OrganizationResult{AssessmentID: id, FailureCode: "stale_input", FreshnessRejection: &rejection}, Error: &contract.FreshnessError{Rejection: rejection}})
	})
	require.Contains(t, console, "topic_semantic_assessment_failed")
	require.NotContains(t, console, "topic_semantic_assessment_completed")
	var payload []byte
	var rejected bool
	for _, record := range records {
		if record["msg"] == "topic_semantic_assessment_rejected" {
			rejected = true
			require.Equal(t, "WARN", record["level"])
			require.Equal(t, "stale_input", record["reason"])
			require.Equal(t, "not_committed", record["transaction_outcome"])
		}
		if chunk, ok := record["payload_chunk"].(map[string]any); ok {
			payload = append(payload, []byte(chunk["body"].(string))...)
		}
	}
	require.True(t, rejected)
	var body struct {
		Result contract.OrganizationResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(payload, &body))
	require.Equal(t, &rejection, body.Result.FreshnessRejection)
	require.Nil(t, body.Result.Publication)
}

func TestTopicDiagnosticLogsNeverTriggerCredentialProtectionUnsupported(t *testing.T) {
	const secret = "topic-log-operational-secret"
	team, id, recordID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var console bytes.Buffer
	sink := &ontologyDiagnosticTestSink{}
	protector := observability.NewCredentialProtector(secret)
	root := observability.NewConsoleWithProtector(slog.LevelDebug, protector, &console)
	require.NoError(t, root.AttachSink(sink))
	logger := root.ContextSlog()
	ctx := observability.WithExchangeScope(context.Background(), observability.ExchangeScope{TeamID: team, OperationID: uuid.NewString(), RunID: uuid.NewString(), BatchID: uuid.NewString()})

	httpMS := int64(42)
	expectedCount, actualCount := 2, 1
	observe := topics.MaintenanceDiagnostics(sink, protector, logger).Observe
	observe(ctx, assessment.Diagnostic{
		Stage: "completed", TeamID: team, Model: "test-model",
		Result: &contract.OrganizationResult{
			AssessmentID: id, FailureCode: "response_invalid",
			Attempts: []contract.AssessmentAttempt{{
				Number: 1, EstimatedInputTokens: 100, EstimatedOutputTokens: 20, FailureCode: "response_invalid",
				Diagnostics: &modelprovider.CallDiagnostics{HTTPMS: &httpMS},
				Validation:  &contract.AssessmentValidation{Stage: "schema", Code: "missing_item", FieldFamily: "items", ExpectedCount: &expectedCount, ActualCount: &actualCount},
			}},
		},
		Error: errors.New("assessment failed with " + secret),
	})

	publishObserve := topics.PublicationDiagnostics(logger, protector)
	publishObserve(ctx, contract.PublicationDiagnostic{
		TeamID: team, OperationID: uuid.NewString(), Action: "publish_automatic", InputState: "captured", Input: []byte(`{}`),
		Result: &contract.PublicationResult{ID: uuid.NewString(), Revision: 3, Records: []contract.RevisionRef{{ID: recordID, Version: 2}}},
	})

	require.NotContains(t, console.String(), secret)
	require.NotContains(t, console.String(), "diagnostic_unavailable_reason")
	var sawAssessmentFailed, sawPublicationCommitted bool
	for _, record := range sink.records {
		require.NotContains(t, record.Attrs, "diagnostic_unavailable_reason", "record %s should not hit CredentialProtectionUnsupported", record.Message)
		encoded, err := json.Marshal(record.Attrs)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), secret)
		switch record.Message {
		case "topic_semantic_assessment_failed":
			sawAssessmentFailed = true
			attempts, ok := record.Attrs["attempts"].([]any)
			require.True(t, ok, "attempts must remain structured array, got %T", record.Attrs["attempts"])
			require.Len(t, attempts, 1)
		case "topic_semantic_publication_committed":
			sawPublicationCommitted = true
			revisions, ok := record.Attrs["revisions"].([]any)
			require.True(t, ok, "revisions must remain structured array, got %T", record.Attrs["revisions"])
			require.Len(t, revisions, 1)
		}
	}
	require.True(t, sawAssessmentFailed)
	require.True(t, sawPublicationCommitted)
}
