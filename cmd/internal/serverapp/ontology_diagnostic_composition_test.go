package serverapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

type ontologyDiagnosticTestSink struct {
	records []observability.LogRecord
	err     error
}

func TestOntologyFreshnessCaptureRetainsCompleteProtectedComparisons(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			sink := &ontologyDiagnosticTestSink{}
			secret := uuid.NewString()
			var console bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&console, &slog.HandlerOptions{Level: level}))
			diagnostic := ontologyDiagnosticCapture(sink, observability.NewCredentialProtector(secret), logger.ContextSlog())
			claim := contract.MaintenanceClaim{TeamID: uuid.NewString(), RunID: uuid.NewString(), ID: uuid.NewString()}
			state := &contract.OrganizationContextState{Records: []contract.OrganizationContextRevision{{ID: secret, Version: 1, Current: true}}, Candidates: []contract.OrganizationContextRevision{}}
			for range contract.MaxDependencyRecords - 1 {
				state.Records = append(state.Records, contract.OrganizationContextRevision{ID: uuid.NewString(), Version: 1, Current: true})
			}
			rejection := &contract.FreshnessRejection{Check: "context_hash", Expected: "sha256:" + strings.Repeat("a", 64), Observed: "sha256:" + strings.Repeat("b", 64), ExpectedContext: state, ObservedContext: state}
			result := contract.OrganizationResult{AssessmentID: uuid.NewString(), FailureCode: "stale_input", FreshnessRejection: rejection}
			require.NoError(t, diagnostic.RecordFailure(context.Background(), claim, result, nil))
			manifest := sink.records[len(sink.records)-1]
			require.Equal(t, float64(0), manifest.Attrs["record_count"])
			require.Equal(t, float64(len(sink.records)-1), manifest.Attrs["freshness_record_count"])
			var body strings.Builder
			var digest string
			for index, record := range sink.records[:len(sink.records)-1] {
				require.Equal(t, "ontology_freshness_payload", record.Message)
				require.Equal(t, claim.TeamID, record.TeamID)
				require.Equal(t, result.AssessmentID, record.Attrs["assessment_id"])
				encoded, err := json.Marshal(record.Attrs)
				require.NoError(t, err)
				require.LessOrEqual(t, len(encoded), observability.MaxOperationMetadataBytes)
				require.NotContains(t, string(encoded), secret)
				var payload struct {
					Chunk observability.DiagnosticBodyChunk `json:"payload_chunk"`
				}
				require.NoError(t, json.Unmarshal(encoded, &payload))
				require.Equal(t, index, payload.Chunk.Index)
				require.Equal(t, len(sink.records)-1, payload.Chunk.Count)
				require.Equal(t, "captured", payload.Chunk.State)
				body.WriteString(payload.Chunk.Body)
				digest = payload.Chunk.SHA256
			}
			expected, err := json.Marshal(rejection)
			require.NoError(t, err)
			require.Equal(t, strings.ReplaceAll(string(expected), secret, observability.CredentialProtectionRedacted), body.String())
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(body.String()))), digest)
			if level == slog.LevelDebug {
				require.Contains(t, console.String(), "ontology_freshness_payload")
				require.NotContains(t, console.String(), secret)
			} else {
				require.Empty(t, console.String())
			}
		})
	}
}

func (s *ontologyDiagnosticTestSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, record)
	return nil
}

func TestOntologyFailureCaptureUsesPrivateSinkAndCompleteManifests(t *testing.T) {
	sink := &ontologyDiagnosticTestSink{}
	const secret = "known-provider-key"
	const authentication = "presented-request-credential"
	ctx := observability.WithAuthenticationSecrets(context.Background(), authentication)
	diagnostic := ontologyDiagnosticCapture(sink, observability.NewCredentialProtector(secret), nil)
	claim := contract.MaintenanceClaim{TeamID: uuid.NewString(), RunID: uuid.NewString(), ID: uuid.NewString()}
	result := contract.OrganizationResult{AssessmentID: uuid.NewString(), FailureCode: "provider_response_invalid"}
	response := `{"error":"` + strings.Repeat("provider detail ", 1000) + secret + authentication + `"}`
	recorder := diagnostic.NewRecorder()
	recorder.RecordProviderExchange(ctx, modelprovider.ProviderExchange{Component: "assessor-" + secret, Model: secret, RequestBody: []byte(`{"messages":"admitted","secret":"user content"}`), ResponseBody: []byte(response), StatusCode: 409, Outcome: "captured"})
	require.NoError(t, diagnostic.RecordFailure(ctx, claim, result, recorder.Snapshot()))
	require.Greater(t, len(sink.records), 2)
	manifest := sink.records[len(sink.records)-1]
	require.Equal(t, "ontology_failure_capture", manifest.Message)
	require.Equal(t, float64(len(sink.records)-1), manifest.Attrs["record_count"])
	var responseBody strings.Builder
	var responseChunks []observability.DiagnosticBodyChunk
	for _, record := range sink.records {
		require.Equal(t, "DEBUG", record.Severity)
		require.Equal(t, claim.TeamID, record.TeamID)
		require.Equal(t, claim.RunID, record.Attrs["reference_id"])
		require.Equal(t, result.AssessmentID, record.Attrs["assessment_id"])
		encoded, err := json.Marshal(record.Attrs)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), observability.MaxOperationMetadataBytes)
		require.NotContains(t, string(encoded), secret)
		require.NotContains(t, string(encoded), authentication)
		var payload struct {
			Direction string                            `json:"direction"`
			Chunk     observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal(encoded, &payload))
		if payload.Direction == "response" {
			responseBody.WriteString(payload.Chunk.Body)
			responseChunks = append(responseChunks, payload.Chunk)
		}
	}
	want := strings.NewReplacer(secret, observability.CredentialProtectionRedacted, authentication, observability.CredentialProtectionRedacted).Replace(response)
	require.Equal(t, want, responseBody.String())
	for index, chunk := range responseChunks {
		require.Equal(t, index, chunk.Index)
		require.Equal(t, len(responseChunks), chunk.Count)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(want))), chunk.SHA256)
	}
	sink.err = errors.New("sink unavailable")
	require.Error(t, diagnostic.RecordFailure(context.Background(), claim, result, recorder.Snapshot()))
}
