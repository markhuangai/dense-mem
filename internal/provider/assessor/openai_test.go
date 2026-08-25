package assessorprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestOpenAIAssessorUsesClosedRememberContract(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				JSONSchema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&body)) {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		assert.Equal(t, "assessor-model", body.Model)
		assert.Len(t, body.Messages, 2)
		assert.Contains(t, body.Messages[0].Content, "integrated structure and support assessor")
		assert.Equal(t, assessor.SemanticAssessmentSchemaName, body.ResponseFormat.JSONSchema.Name)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"content": "{\"request_id\":\"request-1\",\"security_signals\":[],\"entity_results\":[],\"relationship_results\":[]}",
			}}},
			"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		AIVerifierAPIURL:         server.URL,
		AIVerifierAPIKey:         "test-key",
		AIVerifierModel:          "assessor-model",
		AIVerifierMaxConcurrency: 1,
	}
	provider := NewOpenAIAssessor(cfg, server.Client())
	request := assessor.SemanticAssessmentRequest{
		RequestID: "request-1",
		TeamID:    "team-1",
		Evidence: []assessor.SemanticReviewEvidence{{
			EvidenceID: "evidence-1",
			Content:    "Dense-Mem uses PostgreSQL.",
		}},
	}
	session, turn, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Empty(t, turn.ValidationErrors)
	require.Equal(t, 1, turn.Turn)
	require.Equal(t, 1, requestCount)
	require.Equal(t, "request-1", turn.Response.RequestID)
	require.Greater(t, turn.InputTokens, 0)
	require.Greater(t, turn.OutputTokens, 0)
}

func TestOpenAIAssessorCompleteAndStructuredJSONCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": `{"ok":true}`}}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer server.Close()

	provider := NewOpenAIAssessor(&config.Config{
		AIVerifierAPIURL:         server.URL,
		AIVerifierAPIKey:         "test-key",
		AIVerifierModel:          "assessor-model",
		AIVerifierMaxConcurrency: 1,
	}, server.Client())
	require.Equal(t, "assessor-model", provider.ModelName())

	result, err := provider.Complete(context.Background(), modelprovider.StructuredRequest{
		Model:      "assessor-model",
		Messages:   []modelprovider.Message{{Role: "user", Content: "return JSON"}},
		SchemaName: "test_schema",
		Schema:     map[string]any{"type": "object"},
	})
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, result.Content)
	require.Equal(t, 3, result.PromptTokens)
	require.Equal(t, 2, result.CompletionTokens)
	require.Equal(t, 5, result.TotalTokens)

	content, err := provider.openAIStructuredChatJSON(
		context.Background(), "assessor-model", "test_schema", map[string]any{"type": "object"}, "system", map[string]any{"input": "value"},
	)
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, content)
}

func TestOpenAIAssessorUsesProvidedConcurrencyGate(t *testing.T) {
	gate := modelprovider.NewConcurrencyGate(2)
	provider := NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate(
		&config.Config{AIVerifierModel: "assessor-model"}, nil, assessor.DefaultSemanticAssessmentLimits(), gate,
	)
	require.Equal(t, gate, provider.sem)
}

func TestOpenAIAssessorStructuredJSONReportsLocalMarshalAndRequestErrors(t *testing.T) {
	provider := NewOpenAIAssessor(&config.Config{
		AIVerifierAPIURL: "https://example.invalid",
		AIVerifierAPIKey: "test-key",
		AIVerifierModel:  "assessor-model",
	}, nil)

	_, err := provider.openAIStructuredChatJSON(context.Background(), "model", "schema", map[string]any{"invalid": func() {}}, "system", map[string]any{})
	require.ErrorContains(t, err, "failed to marshal structured schema")
	_, err = provider.openAIStructuredChatJSON(context.Background(), "model", "schema", map[string]any{}, "system", func() {})
	require.ErrorContains(t, err, "failed to marshal user payload")

	provider.baseURL = "://invalid"
	_, err = provider.openAIStructuredChatJSON(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorContains(t, err, "failed to create HTTP request")
}
