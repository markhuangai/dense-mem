package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
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
				observe := ontologyAssessmentDiagnostics(root.ContextSlog(), protector)
				request := assessment.Request{RequestID: id, Items: []assessment.Item{{Ref: "s0", PreferredDefinitionRef: "d0", Context: map[string]string{
					"token": "exact admitted token", "password": "exact admitted password", "secret": "exact admitted secret",
					"content": strings.Repeat("admitted source context ", 250) + secret}}}}
				observe(ctx, assessment.Diagnostic{Stage: "prepared", TeamID: team, Request: &request, Model: "test-model"})
				observe(ctx, assessment.Diagnostic{Stage: "rejected", Request: &request, Attempt: 1, Error: errors.New("invalid hierarchy " + secret)})
				observe(ctx, assessment.Diagnostic{Stage: "completed", Request: &request, Result: &contract.OrganizationResult{AssessmentID: id, Current: true}})
			})
			require.NotContains(t, console, secret)
			require.Contains(t, console, "ontology_semantic_assessment_rejected")
			require.Contains(t, console, "ontology_semantic_assessment_completed")
			require.NotContains(t, console, "ontology_semantic_assessment_failed")
			var body []byte
			count := 0
			for _, record := range records {
				require.Equal(t, team, record["team_id"])
				require.Equal(t, operation, record["operation_id"])
				require.Equal(t, id, record["assessment_id"])
				if record["msg"] != "ontology_semantic_assessment" || record["stage"] != "prepared" || record["payload_chunk"] == nil {
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
		observe := ontologyAssessmentDiagnostics(root.ContextSlog(), protector)
		request := assessment.Request{RequestID: uuid.NewString(), Items: []assessment.Item{{Text: strings.Repeat("x", 16*1024*1024+1)}}}
		observe(context.Background(), assessment.Diagnostic{Stage: "completed", Request: &request,
			Result: &contract.OrganizationResult{AssessmentID: request.RequestID, FailureCode: "stale_input"}, Error: contract.ErrSourceStale})
	})
	require.Contains(t, console, "ontology_semantic_assessment_capture_incomplete")
	require.Contains(t, console, "body_bytes_exceeded")
	require.Contains(t, console, "ontology_semantic_assessment_failed")
	require.NotContains(t, console, "ontology_semantic_assessment_completed")
	require.NotContains(t, console, strings.Repeat("x", 100))
}

func TestOntologySemanticAssessmentDefinitionVersionRejection(t *testing.T) {
	id, recordID := uuid.NewString(), uuid.NewString()
	rejection := contract.FreshnessRejection{Check: "dependency_version", RecordID: recordID, Expected: "1", Observed: "2"}
	console, records := captureOntologyPublicationConsole(t, func() {
		protector := observability.NewCredentialProtector()
		root := observability.NewWithProtector(slog.LevelDebug, protector)
		ontologyAssessmentDiagnostics(root.ContextSlog(), protector)(context.Background(), assessment.Diagnostic{Stage: "completed",
			Result: &contract.OrganizationResult{AssessmentID: id, FailureCode: "stale_input", FreshnessRejection: &rejection}, Error: &contract.FreshnessError{Rejection: rejection}})
	})
	require.Contains(t, console, "ontology_semantic_assessment_failed")
	require.NotContains(t, console, "ontology_semantic_assessment_completed")
	var payload []byte
	var rejected bool
	for _, record := range records {
		if record["msg"] == "ontology_semantic_assessment_rejected" {
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
