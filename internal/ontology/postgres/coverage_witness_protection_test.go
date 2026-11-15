//go:build integration

package postgres

import (
	"encoding/json"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func writeCoverageWitnessDiagnostic(t *testing.T, recorder *organizationDiagnosticRecorder, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	protected, reason := recorder.protector.ProtectDiagnosticBytes(encoded, modelprovider.MaxProviderDiagnosticBodyBytes)
	require.Equal(t, observability.CredentialProtectionAvailable, reason)
	writeOrganizationDiagnostic(t, path, json.RawMessage(protected))
}

func TestOrganizationDiagnosticCoverageWitnessResultProtection(t *testing.T) {
	cfg := config.Config{PostgresDSN: "postgres://testuser:testpass@localhost/test", AIVerifierAPIKey: "synthetic-provider-secret"}
	directory := t.TempDir()
	recorder := newOrganizationDiagnosticRecorder(t, directory, "typed-result", cfg)
	path := filepath.Join(directory, "result.json")
	context := ontology.OrganizationContext{Sources: []ontology.SourceSnapshot{{SourceHandle: ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: "source", Version: 9007199254740993}, State: map[string]string{"content": cfg.AIVerifierAPIKey}}}}
	writeCoverageWitnessDiagnostic(t, recorder, path, map[string]any{"context": context, "request": assessment.Request{RequestID: "captured-assessment"}, "original_receipt": ontology.OrganizationReceipt{ID: "original-assessment"}})
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), cfg.AIVerifierAPIKey)
	require.Contains(t, string(body), observability.CredentialProtectionRedacted)
	require.Contains(t, string(body), "9007199254740993")
	var result struct {
		Context ontology.OrganizationContext `json:"context"`
		Request assessment.Request           `json:"request"`
		Receipt ontology.OrganizationReceipt `json:"original_receipt"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	require.Equal(t, context.Sources[0].Version, result.Context.Sources[0].Version)
	require.Equal(t, "captured-assessment", result.Request.RequestID)
	require.Equal(t, "original-assessment", result.Receipt.ID)
}
