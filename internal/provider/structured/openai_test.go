package structured

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func transportConfig(url, key, model string) *config.Config {
	return &config.Config{AIVerifierAPIURL: url, AIVerifierAPIKey: key, AIVerifierModel: model}
}

func transportRequest(model string) modelprovider.StructuredRequest {
	return modelprovider.StructuredRequest{Model: model, SchemaName: "schema", Schema: map[string]any{"type": "object"}, Messages: []modelprovider.Message{{Role: "user", Content: "{}"}}}
}

type assessorRoundTripper func(*http.Request) (*http.Response, error)

func (f assessorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type assessorErrorReader struct{}

func (assessorErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

type assessorExchangeRecorder struct {
	exchanges []modelprovider.ProviderExchange
}

func (r *assessorExchangeRecorder) RecordProviderExchange(_ context.Context, exchange modelprovider.ProviderExchange) {
	r.exchanges = append(r.exchanges, exchange)
}

func TestStructuredTransportNotifiesAdmissionOnlyAfterGateAcquisition(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
	}))
	defer server.Close()

	gate := modelprovider.NewConcurrencyGate(1)
	provider := NewOpenAIWithModel(
		&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "test-key", AIVerifierModel: "model"},
		server.Client(), gate, "model",
	)
	require.NoError(t, modelprovider.AcquireConcurrency(context.Background(), gate))
	called := false
	ctx, cancel := context.WithCancel(modelprovider.WithAdmissionCallback(context.Background(), func(context.Context) error {
		called = true
		return nil
	}))
	cancel()
	_, err := provider.Complete(ctx, modelprovider.StructuredRequest{
		Model: "model", Messages: []modelprovider.Message{{Role: "user", Content: "return JSON"}},
		SchemaName: "test_schema", Schema: map[string]any{"type": "object"},
	})
	var timeout *modelprovider.TimeoutError
	require.ErrorAs(t, err, &timeout)
	require.False(t, called, "a canceled gate waiter must not be reported as admitted")
	require.Zero(t, requestCount)
	modelprovider.ReleaseConcurrency(gate)

	wantErr := errors.New("dispatch marker failed")
	_, err = provider.Complete(modelprovider.WithAdmissionCallback(context.Background(), func(context.Context) error {
		return wantErr
	}), modelprovider.StructuredRequest{
		Model: "model", Messages: []modelprovider.Message{{Role: "user", Content: "return JSON"}},
		SchemaName: "test_schema", Schema: map[string]any{"type": "object"},
	})
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, requestCount, "a failed dispatch marker must prevent the provider request")
}

func TestStructuredTransportRecordsProviderUsageBeforeRejectingContent(t *testing.T) {
	rate := 1_000_000.0
	metrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(context.Context) (observability.AIPricing, error) {
		return observability.AIPricing{
			VerifierModel:                     "remember-override-model",
			VerifierInputUSDPerMillionTokens:  &rate,
			VerifierOutputUSDPerMillionTokens: &rate,
		}, nil
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIWithModel(
		transportConfig(srv.URL, "key", "verifier-fallback-model"),
		srv.Client(),
		nil,
		"remember-override-model",
	)
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationSemanticAssessment, 1)
	_, err := v.Complete(ctx, transportRequest(v.ModelName()))
	require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_cost_usd_total{") {
			continue
		}
		assert.Contains(t, line, `operation="semantic_assessment"`)
		assert.Contains(t, line, `component="verifier"`)
		assert.Contains(t, line, `model="remember-override-model"`)
		assert.True(t, strings.HasSuffix(line, " 15"), "cost line = %q; want 15 USD", line)
		return
	}
	t.Fatal("provider usage did not produce an AI operation cost sample")
}

func TestStructuredTransportMarksMissingUsageBeforeRejectingContent(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
	}))
	defer srv.Close()

	v := NewOpenAI(transportConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationSemanticAssessment, 1)
	_, err := v.Complete(ctx, transportRequest(v.ModelName()))
	require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_unpriced_total{") {
			continue
		}
		if strings.Contains(line, `operation="semantic_assessment"`) &&
			strings.Contains(line, `component="verifier"`) &&
			strings.Contains(line, `model="assessor-model"`) &&
			strings.Contains(line, `reason="missing_usage"`) {
			assert.True(t, strings.HasSuffix(line, " 1"), "unpriced line = %q; want one missing-usage observation", line)
			return
		}
	}
	t.Fatal("missing provider usage did not produce an unpriced operation sample")
}

func TestStructuredTransportEnvelopeDecodeBound(t *testing.T) {
	_, err := decodeOpenAIVerifierAPIResponse(assessorErrorReader{})
	require.ErrorContains(t, err, "read failed")

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader("not-json"))
	require.Error(t, err)

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader(strings.Repeat("x", openAIVerifierMaxResponseBytes+1)))
	require.ErrorContains(t, err, "transport limit")
}

func TestStructuredCaptureLogsInterruptedAndOverBudgetResponses(t *testing.T) {
	for _, name := range []string{"interrupted", "over_budget", "authentication"} {
		t.Run(name, func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "stdout-")
			require.NoError(t, err)
			defer output.Close()
			old := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = old }()
			protector := observability.NewCredentialProtector("synthetic-provider-auth")
			root := observability.NewWithProtector(observability.LevelTrace, protector)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch name {
				case "interrupted":
					w.Header().Set("Content-Length", "12345")
					_, _ = w.Write([]byte("incomplete response with protected value synthetic-provider-auth"))
					w.(http.Flusher).Flush()
				case "over_budget":
					_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes+1)))
				case "authentication":
					http.Error(w, "fixture rejected configured authentication", http.StatusUnauthorized)
				}
			}))
			defer upstream.Close()
			client := observability.DiagnosticHTTPClient(upstream.Client(), root.ContextSlog(), protector)
			transport := NewOpenAI(transportConfig(upstream.URL, "synthetic-provider-auth", "fixture"), client)
			recorder := observability.NewProviderExchangeRecorder(protector)
			ctx := modelprovider.WithExchangeRecorder(t.Context(), recorder)
			result, failure := transport.Complete(ctx, transportRequest("fixture"))
			require.Error(t, failure)
			require.Empty(t, transport.sem)
			exchanges := recorder.Snapshot()
			require.Len(t, exchanges, 1)
			_, err = output.Seek(0, io.SeekStart)
			require.NoError(t, err)
			raw, err := io.ReadAll(output)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "synthetic-provider-auth")
			switch name {
			case "interrupted":
				require.Equal(t, "interrupted", exchanges[0].CaptureState)
				require.Contains(t, string(raw), "\"transport_capture_state\":\"interrupted\"")
				require.Contains(t, string(raw), "incomplete response with protected value")
			case "over_budget":
				require.Equal(t, "unavailable", exchanges[0].CaptureState)
				require.Contains(t, string(raw), "\"state\":\"unavailable\"")
				require.Contains(t, string(raw), "\"reason_code\":\"body_bytes_exceeded\"")
			case "authentication":
				require.Equal(t, http.StatusUnauthorized, result.StatusCode)
				require.Equal(t, modelprovider.ProviderFailureClassHTTPClient, modelprovider.ProviderFailureDetails(failure).Class)
				require.Contains(t, string(raw), "fixture rejected configured authentication")
			}
		})
	}
}
