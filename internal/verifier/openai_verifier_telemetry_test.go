package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStructuredChatUsesTokenizerForIncompleteProviderUsage(t *testing.T) {
	tests := []struct {
		name             string
		promptTokens     int
		completionTokens int
		totalTokens      int
	}{
		{name: "zero usage"},
		{name: "missing output usage", promptTokens: 10, totalTokens: 10},
		{name: "missing input usage", completionTokens: 4, totalTokens: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rate := 1_000_000.0
			metrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(context.Context) (observability.AIPricing, error) {
				return observability.AIPricing{
					VerifierInputUSDPerMillionTokens:  &rate,
					VerifierOutputUSDPerMillionTokens: &rate,
				}, nil
			}))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": `{}`}}},
					"usage": map[string]any{
						"prompt_tokens":     tt.promptTokens,
						"completion_tokens": tt.completionTokens,
						"total_tokens":      tt.totalTokens,
					},
				}))
			}))
			defer srv.Close()

			v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
			v.SetMetrics(metrics)
			ctx := observability.WithAIOperation(context.Background(), observability.AIOperationPlacementAssessment, 1)
			result, err := v.openAIStructuredChatJSONWithUsage(ctx, "assessor-model", "schema", map[string]any{}, "system", map[string]any{})
			require.NoError(t, err)
			require.Nil(t, result.Usage)
			require.NotNil(t, result.ReportedUsage)

			recorder := httptest.NewRecorder()
			metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			body := recorder.Body.String()
			assert.NotContains(t, body, `source="provider"`)
			assert.NotContains(t, body, `reason="missing_usage"`)
			if tt.totalTokens > 0 {
				got, found := verifierMetricLineValue(body, "densemem_verifier_tokens_total", `kind="total"`, `model="assessor-model"`)
				require.True(t, found, "missing provider-reported verifier total tokens\n%s", body)
				assert.Equal(t, float64(tt.totalTokens), got)
			}
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, "densemem_ai_operation_cost_usd_total{") || !strings.Contains(line, `source="tokenizer"`) {
					continue
				}
				fields := strings.Fields(line)
				require.Len(t, fields, 2, "cost line = %q", line)
				cost, err := strconv.ParseFloat(fields[1], 64)
				require.NoError(t, err)
				assert.Positive(t, cost, "cost line = %q", line)
				return
			}
			t.Fatal("incomplete provider usage did not produce a tokenizer cost sample")
		})
	}
}

func TestOpenAIStructuredChatDoesNotMarkMissingUsageForMalformedProviderError(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, err := w.Write([]byte("{not-json"))
		require.NoError(t, err)
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationPlacementAssessment, 1)
	_, err := v.openAIStructuredChatMessagesJSONWithUsage(
		ctx,
		"assessor-model",
		"schema",
		map[string]any{},
		[]openAIVerifierMessage{{Role: "user", Content: `{}`}},
	)
	require.ErrorIs(t, err, ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	_, found := verifierMetricLineValue(
		recorder.Body.String(),
		"densemem_ai_operation_unpriced_total",
		`operation="placement_assessment"`,
		`component="verifier"`,
		`model="assessor-model"`,
		`reason="missing_usage"`,
	)
	assert.False(t, found, "non-OK provider responses must not count as missing usage")
}

func TestOpenAIStructuredChatMarksMissingUsageForMalformedOKResponse(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("{not-json"))
		require.NoError(t, err)
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationPlacementAssessment, 1)
	_, err := v.openAIStructuredChatMessagesJSONWithUsage(
		ctx,
		"assessor-model",
		"schema",
		map[string]any{},
		[]openAIVerifierMessage{{Role: "user", Content: `{}`}},
	)
	require.ErrorIs(t, err, ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	got, found := verifierMetricLineValue(
		recorder.Body.String(),
		"densemem_ai_operation_unpriced_total",
		`operation="placement_assessment"`,
		`component="verifier"`,
		`model="assessor-model"`,
		`reason="missing_usage"`,
	)
	require.True(t, found, "malformed 200 OK responses without usage should remain unpriced")
	assert.Equal(t, float64(1), got)
}

func TestRecordVerifierTokenizerUsageIncludesStructuredSchema(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	v := NewOpenAIVerifier(newTestVerifierConfig("", "", "assessor-model"), nil)
	v.SetMetrics(metrics)
	request := openAIVerifierRequest{
		Model: "assessor-model",
		Messages: []openAIVerifierMessage{
			{Role: "system", Content: "Return structured JSON."},
			{Role: "user", Content: `{"evidence":"schema bytes must be priced"}`},
		},
		ResponseFormat: openAIVerifierResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIVerifierJSONSchema{
				Name:   "large_schema",
				Strict: true,
				Schema: json.RawMessage(`{"type":"object","properties":{"explanation":{"type":"string","description":"` + strings.Repeat("bounded schema detail ", 64) + `"}}}`),
			},
		},
	}
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	wantInputTokens, err := CountTokens(string(requestJSON), v.assessmentLimits.Tokenizer)
	require.NoError(t, err)
	messageTokens, err := semanticAssessmentMessageTokens(request.Messages, v.assessmentLimits.Tokenizer)
	require.NoError(t, err)
	require.Greater(t, wantInputTokens, messageTokens)

	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationPlacementAssessment, 1)
	v.recordVerifierTokenizerUsage(ctx, "assessor-model", request, `{}`)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	got, found := verifierMetricLineValue(
		recorder.Body.String(),
		"densemem_ai_operation_tokens_total",
		`operation="placement_assessment"`,
		`component="verifier"`,
		`model="assessor-model"`,
		`kind="input"`,
		`source="tokenizer"`,
	)
	require.True(t, found)
	assert.Equal(t, float64(wantInputTokens), got)
}

func verifierMetricLineValue(body, metric string, requiredLabels ...string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		matches := true
		for _, label := range requiredLabels {
			if !strings.Contains(line, label) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, false
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}
