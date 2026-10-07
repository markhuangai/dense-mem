package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
)

type organizationDiagnosticRecorder struct {
	t         *testing.T
	directory string
	caseID    string
	sequence  int
	protector *observability.CredentialProtector
}

func newOrganizationDiagnosticRecorder(t *testing.T, directory, caseID string, cfg config.Config) *organizationDiagnosticRecorder {
	t.Helper()
	postgres, err := pgconn.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		t.Fatal(&config.ValidationError{Field: "POSTGRES_DSN", Message: "invalid diagnostic connection configuration"})
	}
	return &organizationDiagnosticRecorder{t: t, directory: directory, caseID: caseID, protector: observability.NewCredentialProtector(cfg.PostgresDSN, postgres.Password, cfg.RedisPassword, cfg.AIAPIKey, cfg.AIVerifierAPIKey, cfg.ControlPortalToken, cfg.TelemetryScrapeToken)}
}

func (r *organizationDiagnosticRecorder) RecordProviderExchange(_ context.Context, exchange modelprovider.ProviderExchange) {
	r.sequence++
	require.LessOrEqual(r.t, r.sequence, 3, "diagnostic exceeded complete assessment attempt bound")
	request, requestReason := r.protector.ProtectDiagnosticBytes(exchange.RequestBody, modelprovider.MaxProviderDiagnosticBodyBytes)
	response, responseReason := r.protector.ProtectDiagnosticBytes(exchange.ResponseBody, modelprovider.MaxProviderDiagnosticBodyBytes)
	entry := map[string]any{"case_id": r.caseID, "sequence": r.sequence, "diagnostic_only": true, "ineligible_for_quality_gate": true, "model": exchange.Model, "status_code": exchange.StatusCode, "outcome": exchange.Outcome, "capture_state": exchange.CaptureState, "started_at": exchange.StartedAt.UTC().Format(time.RFC3339Nano), "completed_at": exchange.CompletedAt.UTC().Format(time.RFC3339Nano),
		"request_body": string(request), "response_body": string(response), "request_protection_reason": int(requestReason), "response_protection_reason": int(responseReason), "validation_error": organizationExchangeValidation(exchange)}
	if requestReason == observability.CredentialProtectionAvailable && len(request) > 0 {
		entry["request_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256(request))
		var envelope struct{ Messages []struct{ Content string } }
		if json.Unmarshal(request, &envelope) == nil && len(envelope.Messages) > 0 {
			entry["system_prompt_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(envelope.Messages[0].Content)))
		}
	}
	if responseReason == observability.CredentialProtectionAvailable && len(response) > 0 {
		entry["response_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256(response))
	}
	protected := r.protector.Snapshot(entry, modelprovider.MaxProviderDiagnosticBodyBytes)
	if protected.UnavailableReason != observability.CredentialProtectionAvailable {
		delete(entry, "request_body")
		delete(entry, "response_body")
		entry["capture_unavailable_reason"] = int(protected.UnavailableReason)
		protected = r.protector.Snapshot(entry, modelprovider.MaxProviderDiagnosticBodyBytes)
	}
	if protected.UnavailableReason == observability.CredentialProtectionAvailable {
		entry = protected.Value.(map[string]any)
	} else {
		entry = map[string]any{"case_id": r.caseID, "sequence": r.sequence, "diagnostic_only": true, "ineligible_for_quality_gate": true, "capture_unavailable_reason": int(protected.UnavailableReason)}
	}
	writeOrganizationDiagnostic(r.t, filepath.Join(r.directory, fmt.Sprintf("%s-exchange-%02d.json", r.caseID, r.sequence)), entry)
}

func organizationExchangeValidation(exchange modelprovider.ProviderExchange) string {
	var requestEnvelope struct{ Messages []struct{ Content string } }
	if err := json.Unmarshal(exchange.RequestBody, &requestEnvelope); err != nil || len(requestEnvelope.Messages) < 2 {
		return "diagnostic request envelope unavailable"
	}
	var request assessment.Request
	if err := json.Unmarshal([]byte(requestEnvelope.Messages[1].Content), &request); err != nil {
		return "diagnostic immutable request unavailable"
	}
	var responseEnvelope struct {
		Choices []struct{ Message struct{ Content string } }
	}
	if err := json.Unmarshal(exchange.ResponseBody, &responseEnvelope); err != nil || len(responseEnvelope.Choices) != 1 {
		return "diagnostic response envelope unavailable"
	}
	response, err := assessment.Decode(responseEnvelope.Choices[0].Message.Content)
	if err == nil {
		err = assessment.Validate(request, response)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func writeOrganizationDiagnostic(t *testing.T, path string, value any) {
	t.Helper()
	output, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = file.Write(output)
	closeErr := file.Close()
	require.NoError(t, err)
	require.NoError(t, closeErr)
}

func TestOrganizationDiagnosticCaptureUsesRealProviderValidation(t *testing.T) {
	var calls atomic.Int32
	var exhaust atomic.Bool
	cfg := config.Config{PostgresDSN: "postgres://testuser:testpass@localhost/test", AIVerifierAPIKey: "synthetic-provider-secret", AIVerifierModel: "fixture-model"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
			t.Error("invalid diagnostic fixture request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request assessment.Request
		if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := organizationFixtureResponse(request, nil)
		response.Items[0].Reason = cfg.AIVerifierAPIKey
		if calls.Add(1) < 3 || exhaust.Load() {
			response.Equivalence[0].Relation = "equivalent"
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg.AIVerifierAPIURL = server.URL
	limits := assessorprovider.SemanticAssessmentLimitsForConfig(&cfg)
	provider := assessment.NewProvider(assessorprovider.NewOpenAIAssessorWithAssessmentLimits(&cfg, server.Client(), limits), cfg.AIVerifierModel, limits)
	directory := t.TempDir()
	recorder := newOrganizationDiagnosticRecorder(t, directory, "manager-separation", cfg)
	request := assessment.Request{RequestID: "diagnostic-fixture", Definitions: []assessment.Definition{}, Items: []assessment.Item{{Ref: "a", Kind: ontology.EvidenceSource, Text: "Atlas uses PostgreSQL."}, {Ref: "b", Kind: ontology.EvidenceSource, Text: "Atlas uses PostgreSQL."}}, Pairs: []assessment.Pair{{Ref: "pair", LeftRef: "a", RightRef: "b", RequiredRelation: "distinct"}}}
	_, attempts, err := provider.Assess(modelprovider.WithExchangeRecorder(context.Background(), recorder), request)
	require.NoError(t, err)
	require.Len(t, attempts, 3)
	require.Equal(t, 3, recorder.sequence)
	for sequence := 1; sequence <= 3; sequence++ {
		path := filepath.Join(directory, fmt.Sprintf("manager-separation-exchange-%02d.json", sequence))
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, string(body), cfg.AIVerifierAPIKey)
		require.Contains(t, string(body), observability.CredentialProtectionRedacted)
		var entry map[string]any
		require.NoError(t, json.Unmarshal(body, &entry))
		require.Equal(t, true, entry["diagnostic_only"])
		require.Equal(t, true, entry["ineligible_for_quality_gate"])
		if sequence < 3 {
			require.Equal(t, "equivalence contradicts deterministic context or manager rule", entry["validation_error"])
		} else {
			require.Empty(t, entry["validation_error"])
		}
		require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(entry["request_body"].(string)))), entry["request_sha256"])
		require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(entry["response_body"].(string)))), entry["response_sha256"])
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	exhaust.Store(true)
	failedDirectory := t.TempDir()
	failedRecorder := newOrganizationDiagnosticRecorder(t, failedDirectory, "exhausted", cfg)
	_, attempts, err = provider.Assess(modelprovider.WithExchangeRecorder(context.Background(), failedRecorder), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Len(t, attempts, 3)
	body, err := os.ReadFile(filepath.Join(failedDirectory, "exhausted-exchange-03.json"))
	require.NoError(t, err)
	require.NotContains(t, string(body), cfg.AIVerifierAPIKey)
	var failed map[string]any
	require.NoError(t, json.Unmarshal(body, &failed))
	require.Equal(t, "equivalence contradicts deterministic context or manager rule", failed["validation_error"])
}

func TestOrganizationDiagnosticHashesSurviveAggregateCaptureLimit(t *testing.T) {
	cfg := config.Config{PostgresDSN: "postgres://testuser:testpass@localhost/test", AIVerifierAPIKey: "synthetic-provider-secret"}
	directory := t.TempDir()
	recorder := newOrganizationDiagnosticRecorder(t, directory, "bounded", cfg)
	body, err := json.Marshal(map[string]any{"padding": strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes/2+1), "messages": []any{map[string]any{"content": "system"}, map[string]any{"content": "{}"}}})
	require.NoError(t, err)
	protected, reason := recorder.protector.ProtectDiagnosticBytes(body, modelprovider.MaxProviderDiagnosticBodyBytes)
	require.Equal(t, observability.CredentialProtectionAvailable, reason)
	combined := recorder.protector.Snapshot(map[string]any{"request_body": string(protected), "response_body": string(protected)}, modelprovider.MaxProviderDiagnosticBodyBytes)
	require.Equal(t, observability.CredentialProtectionBudgetExceeded, combined.UnavailableReason)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{RequestBody: body, ResponseBody: body, Model: "fixture-model", StatusCode: http.StatusOK, Outcome: "captured"})
	data, err := os.ReadFile(filepath.Join(directory, "bounded-exchange-01.json"))
	require.NoError(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(protected)), entry["request_sha256"])
	require.Equal(t, entry["request_sha256"], entry["response_sha256"])
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("system"))), entry["system_prompt_sha256"])
	require.EqualValues(t, observability.CredentialProtectionBudgetExceeded, entry["capture_unavailable_reason"])
	require.NotContains(t, entry, "request_body")
	require.NotContains(t, entry, "response_body")
	require.NotContains(t, string(data), cfg.AIVerifierAPIKey)
}
