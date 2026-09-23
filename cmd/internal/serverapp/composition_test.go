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
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestApplicationBundleBuildsEveryCapabilityService(t *testing.T) {
	bundle := buildApplicationBundle(applicationCompositionDependencies{
		Metrics: observability.NoopDiscoverabilityMetrics(),
		Logger:  observability.New(slog.LevelInfo),
	})
	if bundle.Remember == nil || bundle.Recall == nil || bundle.Community == nil || bundle.Lifecycle == nil || bundle.Context == nil || bundle.Dream == nil || bundle.ControlDream == nil || bundle.Graph == nil || bundle.MemoryPack == nil || bundle.RecallFeedback == nil {
		t.Fatal("application composition did not build every capability service")
	}
}

func TestAISessionModelsUseIndependentOverrides(t *testing.T) {
	fallback := aiSessionModels{
		remember:         "verifier-model",
		conflictReview:   "verifier-model",
		dreamGraph:       "verifier-model",
		dreamEvidence:    "verifier-model",
		communitySummary: "verifier-model",
	}
	for name, configure := range map[string]func(*config.Config){
		"remember": func(cfg *config.Config) { cfg.AIRememberModel = "remember-model" },
		"conflict review": func(cfg *config.Config) {
			cfg.AIConflictReviewModel = "conflict-model"
		},
		"dream graph": func(cfg *config.Config) { cfg.AIDreamGraphModel = "dream-graph-model" },
		"dream evidence": func(cfg *config.Config) {
			cfg.AIDreamEvidenceModel = "dream-evidence-model"
		},
		"community summary": func(cfg *config.Config) {
			cfg.AICommunitySummaryModel = "community-model"
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Config{AIVerifierModel: "verifier-model"}
			configure(&cfg)
			got := aiSessionModelsForConfig(&cfg)
			want := fallback
			switch name {
			case "remember":
				want.remember = "remember-model"
			case "conflict review":
				want.conflictReview = "conflict-model"
			case "dream graph":
				want.dreamGraph = "dream-graph-model"
			case "dream evidence":
				want.dreamEvidence = "dream-evidence-model"
			case "community summary":
				want.communitySummary = "community-model"
			}
			if got != want {
				t.Fatalf("session models = %#v, want %#v", got, want)
			}
		})
	}

	all := aiSessionModelsForConfig(&config.Config{
		AIVerifierModel:         "verifier-model",
		AIRememberModel:         "remember-model",
		AIConflictReviewModel:   "conflict-model",
		AIDreamGraphModel:       "dream-graph-model",
		AIDreamEvidenceModel:    "dream-evidence-model",
		AICommunitySummaryModel: "community-model",
	})
	wantAll := aiSessionModels{
		remember:         "remember-model",
		conflictReview:   "conflict-model",
		dreamGraph:       "dream-graph-model",
		dreamEvidence:    "dream-evidence-model",
		communitySummary: "community-model",
	}
	if all != wantAll {
		t.Fatalf("all session models = %#v, want %#v", all, wantAll)
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
	verifierProvider := verifier.NewOpenAIVerifierWithAssessmentLimitsAndConcurrencyGateAndModel(
		cfg, server.Client(), verifier.DefaultSemanticAssessmentLimits(), gate, "conflict-model",
	)

	if err := modelprovider.AcquireConcurrency(context.Background(), gate); err != nil {
		t.Fatalf("acquire shared gate: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := assessorProvider.Complete(canceled, modelprovider.StructuredRequest{
		Model: "dream-graph-model", Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
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
			Model: "dream-graph-model", Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
			SchemaName: "schema", Schema: map[string]any{"type": "object"},
		})
		errs <- err
	}()
	go func() {
		defer wait.Done()
		_, err := verifierProvider.StructuredChatJSON(context.Background(), "community-model", "schema", map[string]any{"type": "object"}, "system", map[string]any{})
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
		AIVerifierAPIURL:        server.URL,
		AIVerifierAPIKey:        "test-key",
		AIVerifierModel:         "verifier-fallback-model",
		AIRememberModel:         "remember-model",
		AIConflictReviewModel:   "conflict-model",
		AIDreamGraphModel:       "dream-graph-model",
		AIDreamEvidenceModel:    "dream-evidence-model",
		AICommunitySummaryModel: "community-model",
	}
	models := aiSessionModelsForConfig(cfg)
	gate := modelprovider.NewConcurrencyGate(1)
	assessorProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGateAndModel(
		cfg, server.Client(), assessor.DefaultSemanticAssessmentLimits(), gate, models.remember,
	)
	verifierProvider := verifier.NewOpenAIVerifierWithAssessmentLimitsAndConcurrencyGateAndModel(
		cfg, server.Client(), verifier.DefaultSemanticAssessmentLimits(), gate, models.conflictReview,
	)

	calls := []func() error{
		func() error {
			_, _, err := assessorProvider.Assess(context.Background(), assessor.SemanticAssessmentRequest{
				RequestID: "remember-request", TeamID: "team", Evidence: []assessor.SemanticReviewEvidence{{EvidenceID: "evidence", Content: "safe evidence"}},
			})
			return err
		},
		func() error {
			_, err := assessorProvider.Complete(context.Background(), modelprovider.StructuredRequest{
				Model: models.dreamGraph, Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
				SchemaName: "dream_graph", Schema: map[string]any{"type": "object"},
			})
			return err
		},
		func() error {
			_, err := assessorProvider.Complete(context.Background(), modelprovider.StructuredRequest{
				Model: models.dreamEvidence, Messages: []modelprovider.Message{{Role: "user", Content: "{}"}},
				SchemaName: "dream_evidence", Schema: map[string]any{"type": "object"},
			})
			return err
		},
		func() error {
			_, err := verifierProvider.Verify(context.Background(), verifier.Request{ProfileID: "profile", Predicate: "claim"})
			return err
		},
		func() error {
			_, err := verifierProvider.StructuredChatJSON(context.Background(), models.communitySummary, "community_summary", map[string]any{"type": "object"}, "system", map[string]any{})
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
		"dream-graph-model",
		"dream-evidence-model",
		"conflict-model",
		"community-model",
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
	)
	if providers == nil || providers.EmbeddingProvider == nil || providers.RetryEmbedding == nil || providers.Convergence == nil || providers.Reconciliation == nil {
		t.Fatal("search composition did not build all provider facets")
	}
}

func TestCompositionHelpersCoverNilPortsAndTelemetryBranches(t *testing.T) {
	if buildConflictQueueApplication(nil) == nil || buildEvidenceConflictApplication(nil) == nil || buildGraphApplication(nil) == nil {
		t.Fatal("nil adapter composition returned nil service")
	}
	if buildConfigurationApplication(nil, nil) == nil || buildSecurityApplication(nil, nil) == nil || buildUsageMetricsApplication(nil, nil) == nil {
		t.Fatal("nil service composition returned nil application")
	}
	if buildAuditApplication(nil) == nil || buildOperationLogApplication(nil, nil) == nil || buildActiveApplicationLogger(slog.LevelInfo, nil) == nil {
		t.Fatal("nil infrastructure composition returned nil application")
	}
	if got := dreamProviderCycleLease(config.Config{AIVerifierTimeoutSeconds: 2}); got <= time.Minute {
		t.Fatalf("dream provider lease = %s", got)
	}
	if _, err := buildConflictReviewApplication(conflictReviewApplicationDependencies{}); err == nil {
		t.Fatal("missing conflict store did not fail review composition")
	}
	if got := (&communitySummaryProvider{}).ModelName(); got != "" {
		t.Fatalf("empty model name = %q", got)
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
