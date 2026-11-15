package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
)

func TestApplicationBundleBuildsEveryCapabilityService(t *testing.T) {
	bundle := buildApplicationBundle(applicationCompositionDependencies{
		Metrics: observability.NoopDiscoverabilityMetrics(),
		Logger:  observability.New(slog.LevelInfo),
	})
	if bundle.Remember == nil || bundle.Recall == nil || bundle.Lifecycle == nil || bundle.Context == nil || bundle.MemoryPack == nil || bundle.RecallFeedback == nil {
		t.Fatal("application composition did not build every capability service")
	}
}

func TestEvidenceOnlyCatalogSessionModelRetainsRememberOverride(t *testing.T) {
	if got := aiSessionModelsForConfig(&config.Config{AIVerifierModel: "default-model"}); got.remember != "default-model" {
		t.Fatalf("fallback model=%q", got.remember)
	}
	if got := aiSessionModelsForConfig(&config.Config{AIVerifierModel: "default-model", AIRememberModel: "remember-model"}); got.remember != "remember-model" {
		t.Fatalf("Remember model=%q", got.remember)
	}
}

func TestConfiguredSessionProvidersShareConcurrencyGate(t *testing.T) {
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, "invalid provider request", http.StatusBadRequest)
			return
		}
		if body.Model == "" {
			http.Error(writer, "missing provider model", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": `{}`}}},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "test-key", AIVerifierModel: "verifier-model",
	}
	gate := modelprovider.NewConcurrencyGate(1)
	assessorProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGateAndModel(
		cfg, server.Client(), assessor.DefaultSemanticAssessmentLimits(), gate, "remember-model",
	)
	sessionProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGateAndModel(cfg, server.Client(), assessor.DefaultSemanticAssessmentLimits(), gate, "session-model")

	if err := modelprovider.AcquireConcurrency(context.Background(), gate); err != nil {
		t.Fatalf("acquire shared gate: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := assessorProvider.Complete(canceled, modelprovider.StructuredRequest{
		Model: "remember-model", Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
		SchemaName: "schema", Schema: map[string]any{"type": "object"},
	})
	if err == nil {
		t.Fatal("canceled shared-gate waiter succeeded")
	}
	if calls.Load() != 0 {
		t.Fatalf("canceled shared-gate waiter made %d provider calls", calls.Load())
	}
	modelprovider.ReleaseConcurrency(gate)

	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := assessorProvider.Complete(context.Background(), modelprovider.StructuredRequest{
			Model: "remember-model", Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
			SchemaName: "schema", Schema: map[string]any{"type": "object"},
		})
		errs <- err
	}()
	go func() {
		defer wait.Done()
		_, err := sessionProvider.Complete(context.Background(), modelprovider.StructuredRequest{Model: "session-model", SchemaName: "schema", Schema: map[string]any{"type": "object"}, Messages: []modelprovider.Message{{Role: "user", Content: "{}"}}})
		errs <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no configured session entered the provider")
	}
	select {
	case <-started:
		t.Fatal("multiple configured sessions entered a one-slot shared gate")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("configured session provider failed: %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent provider requests = %d, want 1", maximum.Load())
	}
}

func TestConfiguredSessionModelsDoNotFallbackAfterProviderFailure(t *testing.T) {
	var requestedModels []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, "invalid provider request", http.StatusBadRequest)
			return
		}
		requestedModels = append(requestedModels, body.Model)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"error": map[string]string{"message": "configured model unavailable"},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		AIVerifierAPIURL: server.URL,
		AIVerifierAPIKey: "test-key",
		AIVerifierModel:  "verifier-fallback-model",
		AIRememberModel:  "remember-model",
	}
	models := aiSessionModelsForConfig(cfg)
	gate := modelprovider.NewConcurrencyGate(1)
	assessorProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGateAndModel(
		cfg, server.Client(), assessor.DefaultSemanticAssessmentLimits(), gate, models.remember,
	)

	calls := []func() error{
		func() error {
			_, _, err := assessorProvider.Assess(context.Background(), assessor.SemanticAssessmentRequest{
				RequestID: "remember-request", TeamID: "team", Evidence: []assessor.SemanticReviewEvidence{{EvidenceID: "evidence", Content: "safe evidence"}},
			})
			return err
		},
		func() error {
			_, err := assessorProvider.Complete(context.Background(), modelprovider.StructuredRequest{Model: models.remember, SchemaName: "schema", Schema: map[string]any{"type": "object"}, Messages: []modelprovider.Message{{Role: "user", Content: "{}"}}})
			return err
		},
	}
	for _, call := range calls {
		if err := call(); err == nil {
			t.Fatal("configured provider failure unexpectedly succeeded")
		}
	}
	wantModels := []string{
		"remember-model",
		"remember-model",
	}
	if len(requestedModels) != len(wantModels) {
		t.Fatalf("provider request count = %d, want %d: %#v", len(requestedModels), len(wantModels), requestedModels)
	}
	for index, want := range wantModels {
		if requestedModels[index] != want {
			t.Fatalf("provider request %d model = %q, want %q", index, requestedModels[index], want)
		}
	}
}

func TestAccessCompositionReusesEarlyAuthenticationBindings(t *testing.T) {
	logger := observability.New(slog.LevelInfo)
	authentication := buildAccessAuthenticationApplication(nil, nil, 1, logger)
	application := buildAccessApplication(accessApplicationDependencies{
		CredentialVerifier: authentication.CredentialVerifier,
		ActivityWriter:     authentication.ActivityWriter,
		Logger:             logger,
	})
	if application.CredentialVerifier != authentication.CredentialVerifier {
		t.Fatal("access composition replaced the early credential verifier")
	}
	if application.ActivityWriter != authentication.ActivityWriter {
		t.Fatal("access composition replaced the early credential activity writer")
	}
}

func TestSearchProviderCompositionBuildsSharedProviderFacets(t *testing.T) {
	providers := buildSearchProviders(
		config.Config{},
		nil,
		nil,
		nil,
		observability.NoopDiscoverabilityMetrics(),
		observability.New(slog.LevelInfo),
		observability.NewCredentialProtector(),
	)
	if providers == nil || providers.EmbeddingProvider == nil || providers.RetryEmbedding == nil || providers.Convergence == nil || providers.Reconciliation == nil {
		t.Fatal("search composition did not build all provider facets")
	}
}

func TestCompositionHelpersCoverNilPortsAndTelemetryBranches(t *testing.T) {
	if buildEvidenceConflictApplication(nil) == nil {
		t.Fatal("nil adapter composition returned nil service")
	}
	if buildConfigurationApplication(nil, nil) == nil || buildSecurityApplication(nil, nil, nil, nil) == nil || buildUsageMetricsApplication(nil, nil) == nil {
		t.Fatal("nil service composition returned nil application")
	}
	if buildAuditApplication(nil) == nil || buildOperationLogApplication(nil, nil) == nil || buildActiveApplicationLogger(slog.LevelInfo, nil) == nil {
		t.Fatal("nil infrastructure composition returned nil application")
	}

	server, err := newTelemetryScrapeServer(nil, "token")
	if err == nil || server != nil {
		t.Fatal("nil telemetry handler was accepted")
	}
	server, err = newTelemetryScrapeServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), " ")
	if err == nil || server != nil {
		t.Fatal("blank telemetry token was accepted")
	}
	server, err = newTelemetryScrapeServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "token")
	if err != nil {
		t.Fatalf("telemetry server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("metrics route was not registered")
	}
	if err := shutdownTelemetryScrapeServer(server); err != nil {
		t.Fatalf("shutdown telemetry server: %v", err)
	}
}

func TestTelemetryListenerUsesRootBackedTransportObservations(t *testing.T) {
	var logs bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server, err := newTelemetryScrapeServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "token", transportLogger(logger))
	if err != nil {
		t.Fatalf("telemetry server: %v", err)
	}
	t.Cleanup(func() { _ = shutdownTelemetryScrapeServer(server) })

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("successful scrape status = %d", response.Code)
	}
	if !strings.Contains(logs.String(), `"msg":"telemetry_http_request"`) || !strings.Contains(logs.String(), `"correlation_id"`) {
		t.Fatalf("root transport observation missing: %s", logs.String())
	}

	logs.Reset()
	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("rejected scrape status = %d", rejected.Code)
	}
	if !strings.Contains(logs.String(), `"msg":"telemetry_http_request"`) {
		t.Fatalf("rejected transport observation missing: %s", logs.String())
	}
}

func TestTelemetryListenerCapturesRecoveredPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server, err := newTelemetryScrapeServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("scrape panic")
	}), "token", transportLogger(logger))
	if err != nil {
		t.Fatalf("telemetry server: %v", err)
	}
	t.Cleanup(func() { _ = shutdownTelemetryScrapeServer(server) })

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(logs.String(), `"msg":"telemetry_http_request"`) || !strings.Contains(logs.String(), `"delivery_stage":"write_observed"`) {
		t.Fatalf("recovered panic transport observation missing: %s", logs.String())
	}
}
