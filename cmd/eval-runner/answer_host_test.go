package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyUnitAnswerRuntimeUsesConfiguredProviderAndProtectsCredentials(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Equal(t, "Bearer verifier-fixture", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "dense-mem-testing", body["model"])
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"accepted":true}`}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}))
	}))
	defer server.Close()
	for _, entry := range tokenDiagnosticsEnvironment(server.URL) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("AI_VERIFIER_MODEL", "dense-mem-testing")
	t.Setenv("AI_VERIFIER_API_URL", server.URL)
	t.Setenv("LOG_LEVEL", "ERROR")
	logger, protector, transport, limits, err := answerModelRuntime("answer-client-fixture", "answer-test", "host")
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, protector)
	require.NotNil(t, transport)
	require.Positive(t, limits.MaxInputTokens)
	result, err := transport.Complete(context.Background(), modelprovider.StructuredRequest{Model: "dense-mem-testing", SchemaName: "fixture", Schema: map[string]any{"type": "object", "properties": map[string]any{"accepted": map[string]any{"type": "boolean"}}}, Messages: []modelprovider.Message{{Role: "user", Content: "Evaluate fixture."}}, MaxInputTokens: 10000, MaxOutputTokens: 100})
	require.NoError(t, err)
	require.JSONEq(t, `{"accepted":true}`, result.Content)
	require.Equal(t, 1, requests)
	protected := protector.Snapshot("answer-client-fixture verifier-fixture", 1024)
	require.NotContains(t, protected.Value, "answer-client-fixture")
	require.NotContains(t, protected.Value, "verifier-fixture")
	require.ErrorContains(t, runEvidenceOnlyHost(context.Background(), evalharness.RunOptions{}, "one_call"), "intake host requires")
	t.Setenv("AI_VERIFIER_MODEL", "other-model")
	_, _, _, _, err = answerModelRuntime("", "", "host")
	require.ErrorContains(t, err, "valid dense-mem-testing configuration")
	t.Setenv("LOG_LEVEL", "invalid")
	require.ErrorContains(t, runEvidenceOnlyHost(context.Background(), evalharness.RunOptions{}, "one_call"), "valid dense-mem-testing configuration")
}

func TestEvidenceOnlyUnitAnswerCampaignRejectsMissingConfiguration(t *testing.T) {
	require.Error(t, runAnswerCampaign(context.Background(), evalharness.RunOptions{}, evalharness.PairCommand{ConfigPath: t.TempDir() + "/missing.json"}, "one_call"))
}
