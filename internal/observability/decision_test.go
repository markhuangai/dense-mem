package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestDecisionDiagnosticRecordsRemainStructuredProtectedAndBounded(t *testing.T) {
	var output bytes.Buffer
	logger := NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stage := modelprovider.DecisionStage{Workload: "security_review", Settings: domain.DecisionRuntimeConfig{Preferences: domain.DecisionTasks{SecurityReview: true}}, Action: "pass", Reason: "all_hazards_below_probability_gate"}
	LogDecisionStage(context.Background(), logger, "operation", stage)
	var record map[string]any
	require.NoError(t, json.NewDecoder(&output).Decode(&record))
	require.Equal(t, true, record["preferences"].(map[string]any)["security_review"])
	protector := NewCredentialProtector("configured-decision-key")
	protected := protector.Snapshot(DecisionLogValue(modelprovider.DecisionReceipt{Model: "configured-decision-key", Diagnostics: &modelprovider.CallDiagnostics{FailurePhase: "response_status"}}), 8192)
	require.Equal(t, CredentialProtectionAvailable, protected.UnavailableReason)
	encoded, err := json.Marshal(protected.Value)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "configured-decision-key")
	require.Contains(t, string(encoded), "response_status")
	require.Contains(t, string(DecisionLogValue(strings.Repeat("x", MaxOperationMetadataBytes))), "diagnostic_body_limit")
}
