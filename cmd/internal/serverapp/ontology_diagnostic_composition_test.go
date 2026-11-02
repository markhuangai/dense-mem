package serverapp

import (
	"context"
	"encoding/json"
	"errors"
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
	diagnostic := ontologyDiagnosticCapture(sink, observability.NewCredentialProtector(secret))
	claim := contract.MaintenanceClaim{TeamID: uuid.NewString(), RunID: uuid.NewString(), ID: uuid.NewString()}
	result := contract.OrganizationResult{AssessmentID: uuid.NewString(), FailureCode: "provider_response_invalid"}
	response := `{"error":"` + strings.Repeat("provider detail ", 1000) + secret + `"}`
	recorder := diagnostic.NewRecorder()
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{Component: "assessor", RequestBody: []byte(`{"messages":"admitted","secret":"user content"}`), ResponseBody: []byte(response), StatusCode: 409, Outcome: "captured"})
	require.NoError(t, diagnostic.RecordFailure(context.Background(), claim, result, recorder.Snapshot()))
	require.Greater(t, len(sink.records), 2)
	manifest := sink.records[len(sink.records)-1]
	require.Equal(t, "ontology_failure_capture", manifest.Message)
	require.Equal(t, float64(len(sink.records)-1), manifest.Attrs["record_count"])
	for _, record := range sink.records {
		require.Equal(t, "DEBUG", record.Severity)
		require.Equal(t, claim.TeamID, record.TeamID)
		require.Equal(t, claim.RunID, record.Attrs["reference_id"])
		require.Equal(t, result.AssessmentID, record.Attrs["assessment_id"])
		encoded, err := json.Marshal(record.Attrs)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), observability.MaxOperationMetadataBytes)
		require.NotContains(t, string(encoded), secret)
	}
	sink.err = errors.New("sink unavailable")
	require.Error(t, diagnostic.RecordFailure(context.Background(), claim, result, recorder.Snapshot()))
}
