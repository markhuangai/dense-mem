package modelprovider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallDiagnosticsKeepUnobservedDistinctFromZeroAndValidateClosedPhases(t *testing.T) {
	var historical CallDiagnostics
	require.NoError(t, json.Unmarshal([]byte(`{}`), &historical))
	require.Nil(t, historical.QueueMS)
	require.True(t, historical.Valid())
	zero, negative := int64(0), int64(-1)
	observed := CallDiagnostics{QueueMS: &zero}
	encoded, err := json.Marshal(observed)
	require.NoError(t, err)
	require.JSONEq(t, `{"queue_ms":0}`, string(encoded))
	for _, value := range []CallDiagnostics{{QueueMS: &negative}, {HTTPMS: &negative}, {AdmissionMS: &negative}, {ReconciliationMS: &negative}, {HTTPTimeoutMS: &negative}, {FailurePhase: "provider payload"}, {ReconciliationFailed: true}} {
		require.False(t, value.Valid())
	}
	require.True(t, (&CallDiagnostics{FailurePhase: "reconciliation", ReconciliationMS: &zero, ReconciliationFailed: true}).Valid())
	var missing *CallDiagnostics
	require.True(t, missing.Valid())
}
