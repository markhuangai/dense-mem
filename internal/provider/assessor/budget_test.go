package assessorprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestSemanticAssessmentAcceptsExactSerializedInputBoundary(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		content, err := json.Marshal(semanticAssessmentTestResponse())
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		})
	}))
	defer server.Close()

	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	request.SubmissionContract.Relationships[0].KnownPredicateKey = "works_on"
	limits := DefaultSemanticAssessmentLimits()
	limits.ProviderModel = "assessor-model"
	limits.ProviderSchemaName = assessor.SemanticAssessmentSchemaName
	limits.ProviderFramingEnabled = true
	limits.MaxInputTokens = 1_000_000
	prepared, errs := assessor.PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	userJSON, err := json.Marshal(prepared)
	require.NoError(t, err)
	exact, err := assessor.CountSemanticAssessmentProviderRequestTokens(
		limits.ProviderModel,
		limits.ProviderSchemaName,
		assessor.SemanticAssessmentResponseSchema(),
		limits.ProviderTemperatureDisabled,
		[]assessor.SemanticAssessmentProviderMessage{
			{Role: "system", Content: assessor.SemanticAssessmentSystemPrompt},
			{Role: "user", Content: string(userJSON)},
		}, limits.Tokenizer,
	)
	require.NoError(t, err)
	limits.MaxInputTokens = exact
	provider := NewOpenAIAssessorWithAssessmentLimits(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client(), limits)
	_, turn, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Empty(t, turn.ValidationErrors)
	require.Equal(t, 1, calls)
}

func TestSemanticAssessmentRepairEnforcesGrowthOfSerializedConversation(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		response := semanticAssessmentTestResponse()
		response.RequestID = "wrong-request"
		content, err := json.Marshal(response)
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		})
	}))
	defer server.Close()

	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	request.SubmissionContract.Relationships[0].KnownPredicateKey = "works_on"
	limits := DefaultSemanticAssessmentLimits()
	limits.ProviderModel = "assessor-model"
	limits.ProviderSchemaName = assessor.SemanticAssessmentSchemaName
	limits.ProviderFramingEnabled = true
	limits.MaxInputTokens = 1_000_000
	prepared, errs := assessor.PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	userJSON, err := json.Marshal(prepared)
	require.NoError(t, err)
	exact, err := assessor.CountSemanticAssessmentProviderRequestTokens(
		limits.ProviderModel, limits.ProviderSchemaName, assessor.SemanticAssessmentResponseSchema(), limits.ProviderTemperatureDisabled,
		[]assessor.SemanticAssessmentProviderMessage{{Role: "system", Content: assessor.SemanticAssessmentSystemPrompt}, {Role: "user", Content: string(userJSON)}}, limits.Tokenizer,
	)
	require.NoError(t, err)
	limits.MaxInputTokens = exact
	provider := NewOpenAIAssessorWithAssessmentLimits(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client(), limits)
	session, first, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.NotEmpty(t, first.ValidationErrors)
	_, err = provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{Request: request, ValidationErrors: first.ValidationErrors})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "input_budget", malformed.FailureClass)
	require.Equal(t, 1, calls)
}

func TestSemanticAssessmentRepairDoesNotDoubleCountFrozenCandidateContext(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		response := semanticAssessmentTestResponse()
		if calls == 1 {
			response.EntityResults[0].GroundingRef = nil
		}
		content, err := json.Marshal(response)
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		})
	}))
	defer server.Close()

	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	limits := DefaultSemanticAssessmentLimits()
	limits.ProviderModel = "assessor-model"
	limits.ProviderSchemaName = assessor.SemanticAssessmentSchemaName
	limits.ProviderFramingEnabled = true
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000
	prepared, errs := assessor.PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	limits.MaxCandidateContextTokens = prepared.CandidateContextTokens
	provider := NewOpenAIAssessorWithAssessmentLimits(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client(), limits)
	session, first, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.NotEmpty(t, first.ValidationErrors)
	second, err := provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{
		Request: request, ValidationErrors: first.ValidationErrors,
	})
	require.NoError(t, err)
	require.Empty(t, second.ValidationErrors)
	require.Equal(t, 2, calls)
}

func TestSemanticAssessmentCandidateContextBudgetIgnoresAssistantFields(t *testing.T) {
	user := openAIVerifierMessage{Role: "user", Content: `{"entity_candidate_groups":[{"evidence_id":"evidence:0","candidates":[{"entity_id":"entity-1"}]}]}`}
	assistant := openAIVerifierMessage{Role: "assistant", Content: `{"entity_candidate_groups":[{"evidence_id":"evidence:1","candidates":[{"entity_id":"` + strings.Repeat("assistant-only ", 200) + `"}]}]}`}
	userOnly, err := semanticAssessmentConversationCandidateContextTokens([]openAIVerifierMessage{user}, "o200k_base")
	require.NoError(t, err)
	withAssistant, err := semanticAssessmentConversationCandidateContextTokens([]openAIVerifierMessage{user, assistant}, "o200k_base")
	require.NoError(t, err)
	require.Equal(t, userOnly, withAssistant)
}
