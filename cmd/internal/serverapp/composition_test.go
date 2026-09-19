package serverapp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
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
