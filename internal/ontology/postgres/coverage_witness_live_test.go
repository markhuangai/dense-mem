//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type coverageProbeRepository struct {
	*Store
	expected []ontology.SourceDependency
}

func (r *coverageProbeRepository) ReadOrganization(ctx context.Context, team string, handles []ontology.SourceHandle) (ontology.OrganizationContext, error) {
	current, err := r.Store.ReadOrganization(ctx, team, handles)
	if err != nil {
		return current, err
	}
	if len(current.Sources) != len(r.expected) {
		return current, errors.New("witness source count no longer matches its retained context")
	}
	for _, source := range current.Sources {
		fingerprint, err := ontology.SourceFingerprint(source)
		if err != nil {
			return current, err
		}
		matched := false
		for _, expected := range r.expected {
			matched = matched || (expected.SourceHandle == source.SourceHandle && expected.Fingerprint == fingerprint)
		}
		if !matched {
			return current, fmt.Errorf("witness source no longer matches its retained version and fingerprint")
		}
	}
	return current, nil
}

type coverageProbeTransport struct {
	modelprovider.StructuredTransport
	calls    atomic.Int32
	maxCalls int32
}

func (p *coverageProbeTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	limit := p.maxCalls
	if limit == 0 {
		limit = 18
	}
	if p.calls.Add(1) > limit {
		return modelprovider.StructuredResult{}, errors.New("diagnostic provider-call limit reached")
	}
	return p.StructuredTransport.Complete(ctx, request)
}

func TestOntologyCoverageCandidateWitness(t *testing.T) {
	input := os.Getenv("DENSE_MEM_ONTOLOGY_CANDIDATE_WITNESS_INPUT")
	if input == "" {
		t.Skip("explicit captured predicate context required")
	}
	require.Equal(t, "1", os.Getenv("DENSE_MEM_ONTOLOGY_LIVE_EVAL"))
	require.Equal(t, "1", os.Getenv("DENSE_MEM_ONTOLOGY_DIAGNOSTIC"))
	var captured struct {
		Family               string                       `json:"family"`
		OriginalAssessmentID string                       `json:"original_assessment_id"`
		Context              ontology.OrganizationContext `json:"context"`
		OriginalReceipt      ontology.OrganizationReceipt `json:"original_receipt"`
	}
	body, err := os.ReadFile(input)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &captured))
	require.Equal(t, "commit_conflict", captured.Family)
	require.Equal(t, captured.OriginalAssessmentID, captured.OriginalReceipt.ID)
	require.Len(t, captured.Context.Sources, 1)
	require.Equal(t, ontology.PredicateSource, captured.Context.Sources[0].Kind)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "dense-mem-testing", cfg.GetAIVerifierModel())
	directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR")
	require.NotEmpty(t, directory)
	require.NoError(t, os.Mkdir(directory, 0700), "candidate probe must use a new directory")
	recorder := newOrganizationDiagnosticRecorder(t, directory, "candidate-predicate", cfg)
	limits := assessorprovider.SemanticAssessmentLimitsForConfig(&cfg)
	transport := &coverageProbeTransport{StructuredTransport: assessorprovider.NewOpenAIAssessorWithAssessmentLimits(&cfg, nil, limits), maxCalls: 3}
	provider := assessment.NewProvider(transport, cfg.GetAIVerifierModel(), limits, 500000)
	require.NoError(t, provider.ConfigurationError())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	started := time.Now()
	request, response, attempts, assessErr := organization.NewService(nil, provider).AssessDiagnostic(modelprovider.WithExchangeRecorder(ctx, recorder), captured.Context)
	entry := map[string]any{"original_assessment_id": captured.OriginalAssessmentID, "context": captured.Context,
		"context_origin": "checkpoint reconstructed snapshot", "historical_context_available": false,
		"reconstructed": true, "diagnostic_only": true, "semantic_commit": false, "ineligible_for_quality_gate": true,
		"request": request, "response": response, "attempts": attempts, "provider_identity": provider.Identity(),
		"provider_calls": transport.calls.Load(), "provider_call_limit": 3, "elapsed_ms": time.Since(started).Milliseconds()}
	if assessErr != nil {
		entry["error"] = assessErr.Error()
	}
	writeCoverageWitnessDiagnostic(t, recorder, filepath.Join(directory, "result.json"), entry)
	require.Len(t, request.Items, 1)
	require.Empty(t, request.Items[0].LockedDefinitionRef, "candidate witness must remain provider-reachable on the normal path")
	require.Positive(t, transport.calls.Load())
	require.LessOrEqual(t, transport.calls.Load(), int32(3))
	require.Equal(t, int(transport.calls.Load()), recorder.sequence)
	require.NoError(t, assessErr)
	require.NoError(t, assessment.Validate(request, response))
}

func TestOntologyCoverageLiveWitnesses(t *testing.T) {
	input := os.Getenv("DENSE_MEM_ONTOLOGY_WITNESS_INPUT")
	if input == "" {
		t.Skip("explicit frozen coverage witness input required")
	}
	require.Equal(t, "1", os.Getenv("DENSE_MEM_ONTOLOGY_LIVE_EVAL"))
	require.Equal(t, "1", os.Getenv("DENSE_MEM_ONTOLOGY_DIAGNOSTIC"))
	var manifest struct {
		SchemaVersion string `json:"schema_version"`
		Scope         struct {
			TeamID     string `json:"team_id"`
			SpaceID    string `json:"space_id"`
			Generation int64  `json:"generation"`
		} `json:"scope"`
		Witnesses []struct {
			Family               string                       `json:"family"`
			OriginalAssessmentID string                       `json:"original_assessment_id"`
			HistoricalContext    bool                         `json:"historical_context_available"`
			OriginalReceipt      ontology.OrganizationReceipt `json:"original_receipt"`
		} `json:"witnesses"`
	}
	body, err := os.ReadFile(input)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.Equal(t, "dense-mem.ontology.coverage_diagnostics.v1", manifest.SchemaVersion)
	families := []string{"request_id_mismatch", "item_locked_classification", "item_ambiguous_classification", "item_reason_limit", "commit_conflict", "classification_ambiguous"}
	require.Len(t, manifest.Witnesses, len(families))
	for i, witness := range manifest.Witnesses {
		require.Equal(t, families[i], witness.Family)
		require.Equal(t, witness.OriginalAssessmentID, witness.OriginalReceipt.ID)
		require.False(t, witness.HistoricalContext)
	}
	cfg, err := config.LoadWithPostgresDSN(os.Getenv("DENSE_MEM_ONTOLOGY_WITNESS_DSN"))
	require.NoError(t, err)
	require.NotEmpty(t, os.Getenv("DENSE_MEM_ONTOLOGY_WITNESS_DSN"))
	require.Equal(t, "dense-mem-testing", cfg.GetAIVerifierModel())
	db, err := gorm.Open(gormpostgres.Open(cfg.PostgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var readOnly string
	require.NoError(t, db.Raw(`SHOW default_transaction_read_only`).Row().Scan(&readOnly))
	require.Equal(t, "on", readOnly, "retained-state diagnostics require a read-only connection")
	store := NewStore(db, storage.NewRLS())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR")
	require.NotEmpty(t, directory)
	require.NoError(t, os.Mkdir(directory, 0700), "a diagnostic campaign must use a new output directory")
	limits := assessorprovider.SemanticAssessmentLimitsForConfig(&cfg)
	transport := &coverageProbeTransport{StructuredTransport: assessorprovider.NewOpenAIAssessorWithAssessmentLimits(&cfg, nil, limits)}
	started := time.Now()
	for _, witness := range manifest.Witnesses {
		t.Run(witness.Family, func(t *testing.T) {
			require.NoError(t, ctx.Err())
			caseDirectory := filepath.Join(directory, witness.Family)
			require.NoError(t, os.Mkdir(caseDirectory, 0700))
			recorder := newOrganizationDiagnosticRecorder(t, caseDirectory, witness.Family, cfg)
			repository := &coverageProbeRepository{Store: store, expected: witness.OriginalReceipt.Sources}
			provider := assessment.NewProvider(transport, cfg.GetAIVerifierModel(), limits, 500000)
			require.NoError(t, provider.ConfigurationError())
			handles := make([]ontology.SourceHandle, 0, len(repository.expected))
			for _, source := range repository.expected {
				handles = append(handles, source.SourceHandle)
			}
			sourceContext, err := repository.ReadOrganization(ctx, manifest.Scope.TeamID, handles)
			require.NoError(t, err, "incomplete witness: current source context unavailable")
			beforeCalls := transport.calls.Load()
			request, response, attempts, assessErr := organization.NewService(nil, provider).AssessDiagnostic(modelprovider.WithExchangeRecorder(ctx, recorder), sourceContext)
			for _, source := range sourceContext.Sources {
				require.Equal(t, manifest.Scope.SpaceID, source.SpaceID)
				require.Equal(t, manifest.Scope.Generation, source.Generation)
			}
			entry := map[string]any{"family": witness.Family, "original_assessment_id": witness.OriginalAssessmentID,
				"historical_context_available": false, "reconstructed": true, "diagnostic_only": true,
				"ineligible_for_quality_gate": true, "semantic_commit": false, "context": sourceContext,
				"original_receipt": witness.OriginalReceipt, "request": request, "response": response,
				"attempts": attempts, "provider_identity": provider.Identity(),
				"provider_calls": transport.calls.Load() - beforeCalls, "elapsed_ms": time.Since(started).Milliseconds()}
			if assessErr != nil {
				entry["error"] = assessErr.Error()
			}
			writeCoverageWitnessDiagnostic(t, recorder, filepath.Join(caseDirectory, "result.json"), entry)
			require.Positive(t, transport.calls.Load()-beforeCalls, "incomplete witness: no live assessment dispatched")
			require.LessOrEqual(t, transport.calls.Load()-beforeCalls, int32(3))
			require.Len(t, attempts, int(transport.calls.Load()-beforeCalls))
			require.Equal(t, len(attempts), recorder.sequence, "incomplete witness: provider exchange missing")
			for sequence := 1; sequence <= recorder.sequence; sequence++ {
				body, err := os.ReadFile(filepath.Join(caseDirectory, fmt.Sprintf("%s-exchange-%02d.json", witness.Family, sequence)))
				require.NoError(t, err)
				var capture map[string]any
				require.NoError(t, json.Unmarshal(body, &capture))
				require.Empty(t, capture["capture_state"], "incomplete witness: truncated exchange")
				require.NotContains(t, capture, "capture_unavailable_reason", "incomplete witness: capture unavailable")
				require.NotEmpty(t, capture["request_body"], "incomplete witness: request body missing")
				require.NotEmpty(t, capture["response_body"], "incomplete witness: response body missing")
			}
		})
	}
	writeOrganizationDiagnostic(t, filepath.Join(directory, "campaign.json"), map[string]any{
		"assessments": len(manifest.Witnesses), "provider_calls": transport.calls.Load(), "provider_call_limit": 18,
		"model": cfg.GetAIVerifierModel(), "elapsed_ms": time.Since(started).Milliseconds(), "semantic_commit": false,
		"reconstructed": true, "diagnostic_only": true, "failed": t.Failed()})
}

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
