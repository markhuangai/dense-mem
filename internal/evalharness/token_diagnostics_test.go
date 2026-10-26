package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestTokenDiagnosticsCollectsRealPrometheusSamplesWithoutAggregation(t *testing.T) {
	registry := prometheus.NewRegistry()
	metricNames := []string{"densemem_operation_provider_tokens_total", "densemem_ai_operation_tokens_total", "densemem_ai_operation_unpriced_total", "densemem_embedding_tokens_total", "densemem_verifier_tokens_total"}
	for index, name := range metricNames {
		counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: "Fixture counter."}, []string{"model", "kind", "team_id", "credential_name"})
		registry.MustRegister(counter)
		counter.WithLabelValues("public-token-model", "input", "private-team-a", "hidden-name").Add(float64(index + 1))
		counter.WithLabelValues("public-token-model", "input", "private-team-b", "hidden-name").Add(float64(index + 10))
	}
	registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "unselected_secret_metric", Help: "Do not export."}))
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scrape-fixture" {
			t.Error("collector did not authenticate")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{
		MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector(),
	})
	require.NoError(t, err)
	var result tokenDiagnosticSnapshot
	require.NoError(t, json.Unmarshal(body, &result))
	require.Equal(t, "observed", result.Status)
	require.Len(t, result.Metrics, 10)
	require.WithinDuration(t, time.Now(), result.ObservedAt, time.Second)
	for index, name := range metricNames {
		var values []float64
		for _, metric := range result.Metrics {
			if metric.Name == name {
				values = append(values, metric.Value)
				require.Equal(t, map[string]string{"model": "public-token-model", "kind": "input"}, metric.Labels)
			}
		}
		require.ElementsMatch(t, []float64{float64(index + 1), float64(index + 10)}, values)
	}
	for _, excluded := range []string{"private-team", "team_id", "credential_name", "hidden-name", "unselected_secret_metric", "Fixture counter", server.URL} {
		require.NotContains(t, string(body), excluded)
	}
}

func TestTokenDiagnosticsProtectsConfiguredAndScrapeSecrets(t *testing.T) {
	secret := `provider/secret<key>`
	for _, label := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), `provider\/secret\u003ckey\u003e`, "scrape-fixture"} {
		t.Run(label, func(t *testing.T) {
			payload := fmt.Sprintf("densemem_ai_operation_tokens_total{model=%s,operation=\"remember\",component=\"assessor\",kind=\"input\",source=\"provider\",reason=\"fictional-password\",extra=\"omitted\"} 42\n", strconv.Quote(label))
			server := tokenDiagnosticServer(t, http.StatusOK, payload)
			body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{
				MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector(secret),
			})
			require.NoError(t, err)
			var result tokenDiagnosticSnapshot
			require.NoError(t, json.Unmarshal(body, &result))
			require.Equal(t, observability.CredentialProtectionRedacted, result.Metrics[0].Labels["model"])
			require.Equal(t, "fictional-password", result.Metrics[0].Labels["reason"])
			require.Len(t, result.Metrics[0].Labels, 6)
			require.Equal(t, float64(42), result.Metrics[0].Value)
		})
	}
}

func TestTokenDiagnosticsRejectsUnsafeURLsAndUnavailableProtectionBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	for _, address := range []string{"", "/metrics", "file:///metrics", "http://user:scrape-fixture@example.test/metrics", server.URL + "?token=scrape-fixture", server.URL + "#fragment", server.URL + "/scrape-fixture", server.URL + "?"} {
		body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{
			MetricsURL: address, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector(),
		})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "scrape-fixture")
		require.Nil(t, body)
	}
	for _, protector := range []*observability.CredentialProtector{nil, observability.NewCredentialProtector(strings.Repeat("x", observability.MaxCredentialSecretBytes+1))} {
		body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: protector})
		require.Error(t, err)
		require.Nil(t, body)
	}
	body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{MetricsURL: server.URL, Protector: observability.NewCredentialProtector()})
	require.ErrorContains(t, err, "scrape token")
	require.Nil(t, body)
	require.Zero(t, calls.Load())
}

func TestTokenDiagnosticsRejectsResponsesWithoutPartialOutput(t *testing.T) {
	valid := "densemem_embedding_tokens_total{model=\"public-model\",kind=\"input\"} 3\n"
	for _, test := range []struct {
		name    string
		status  int
		payload string
		want    string
	}{
		{"unauthorized", 401, "private-response-body", "HTTP 401"},
		{"server failure", 503, "private-response-body", "HTTP 503"},
		{"malformed", 200, valid + "private-response-body{\n", "malformed"},
		{"oversized response", 200, strings.Repeat("x", tokenDiagnosticsResponseLimit+1), "response limit"},
		{"oversized output", 200, fmt.Sprintf("densemem_embedding_tokens_total{model=%q} 1\n", strings.Repeat("x", tokenDiagnosticsOutputLimit)), "output limit"},
		{"nonfinite", 200, "densemem_embedding_tokens_total NaN\n", "finite counter"},
		{"wrong type", 200, "# TYPE densemem_embedding_tokens_total histogram\ndensemem_embedding_tokens_total_bucket{le=\"1\"} 1\n", "counter"},
		{"duplicate label", 200, "densemem_embedding_tokens_total{model=\"one\",model=\"two\"} 1\n", "malformed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := tokenDiagnosticServer(t, test.status, test.payload)
			body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector()})
			require.ErrorContains(t, err, test.want)
			require.NotContains(t, err.Error(), "private-response-body")
			require.NotContains(t, err.Error(), server.URL)
			require.Nil(t, body)
		})
	}
}

func TestTokenDiagnosticsRejectsRedirectsAndHonorsCancellation(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Store(true) }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	t.Cleanup(redirect.Close)
	opts := TokenDiagnosticsOptions{MetricsURL: redirect.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector()}
	body, err := CollectTokenDiagnostics(context.Background(), opts)
	require.ErrorContains(t, err, "HTTP 302")
	require.Nil(t, body)
	require.False(t, redirected.Load())
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(slow.Close)
	opts.MetricsURL = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	body, err = CollectTokenDiagnostics(ctx, opts)
	require.ErrorContains(t, err, "timed out")
	require.Nil(t, body)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	body, err = CollectTokenDiagnostics(ctx, opts)
	require.ErrorContains(t, err, "cancelled")
	require.Nil(t, body)
}

func TestTokenDiagnosticsEmptyAndEncodingFailureAreDistinct(t *testing.T) {
	server := tokenDiagnosticServer(t, 200, "unselected_metric 5\n")
	body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector()})
	require.NoError(t, err)
	var result tokenDiagnosticSnapshot
	require.NoError(t, json.Unmarshal(body, &result))
	require.Equal(t, "empty", result.Status)
	require.Empty(t, result.Metrics)
	require.Contains(t, string(body), `"metrics":[]`)
	encoded := "private-fixture"
	for range 20 {
		encoded = strings.ReplaceAll(encoded, "%", "%25")
		if encoded == "private-fixture" {
			encoded = "%70rivate-fixture"
		}
	}
	server = tokenDiagnosticServer(t, 200, fmt.Sprintf("densemem_embedding_tokens_total{model=%q} 1\n", encoded))
	body, err = CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector("private-fixture")})
	require.ErrorContains(t, err, "protection unavailable")
	require.Nil(t, body)
}

func TestTokenDiagnosticsDoesNotExportRedactedMetricStructure(t *testing.T) {
	server := tokenDiagnosticServer(t, 200, "densemem_embedding_tokens_total{model=\"public-model\"} 123456.75\n")
	for _, secret := range []string{"densemem_embedding_tokens_total", "123456.75", "model"} {
		body, err := CollectTokenDiagnostics(context.Background(), TokenDiagnosticsOptions{
			MetricsURL: server.URL, ScrapeToken: "scrape-fixture", Protector: observability.NewCredentialProtector(secret),
		})
		require.ErrorContains(t, err, "protection unavailable")
		require.Nil(t, body)
	}
}

func tokenDiagnosticServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}
