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

func TestSemanticAssessmentAcceptsSerializedInputWithRepairHeadroom(t *testing.T) {
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
	limits.MaxInputTokens = DefaultSemanticAssessmentLimits().MaxInputTokens
	provider := NewOpenAIAssessorWithAssessmentLimits(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client(), limits)
	_, turn, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Empty(t, turn.ValidationErrors)
	require.Equal(t, exact, turn.InputTokens)
	require.Equal(t, 1, calls)
}

func TestSemanticAssessmentRejectsRequiredInputWithoutRepairHeadroom(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("provider should not be called when preflight rejects missing repair headroom")
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
	_, _, err = provider.Assess(context.Background(), request)
	var providerErr *ProviderError
	require.ErrorAs(t, err, &providerErr)
	require.Equal(t, ProviderFailureClassRequestInvalid, providerErr.FailureClass)
	require.Contains(t, err.Error(), "repair headroom")
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

func TestSemanticAssessmentRepairBoundsEscapedAssistantHistory(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	correction := semanticAssessmentSessionRepair{
		ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "must be valid"}},
		Instruction:      "Return one complete replacement JSON object.",
	}
	correctionJSON, err := json.Marshal(correction)
	require.NoError(t, err)
	messages := []openAIVerifierMessage{
		{Role: "system", Content: assessor.SemanticAssessmentSystemPrompt},
		{Role: "user", Content: `{"request_id":"request"}`},
		{Role: "assistant", Content: strings.Repeat("\x00", 10_000)},
		{Role: "user", Content: string(correctionJSON)},
	}
	rawTokens, err := semanticAssessmentTurnTokens(
		"assessor-model",
		assessor.SemanticAssessmentSchemaName,
		messages,
		assessor.SemanticAssessmentResponseSchema(),
		limits.ProviderTemperatureDisabled,
		limits.Tokenizer,
	)
	require.NoError(t, err)
	placeholderMessages := append([]openAIVerifierMessage(nil), messages...)
	placeholderMessages[2].Content = semanticAssessmentRepairHistoryPlaceholder
	placeholderTokens, err := semanticAssessmentTurnTokens(
		"assessor-model",
		assessor.SemanticAssessmentSchemaName,
		placeholderMessages,
		assessor.SemanticAssessmentResponseSchema(),
		limits.ProviderTemperatureDisabled,
		limits.Tokenizer,
	)
	require.NoError(t, err)
	require.Greater(t, rawTokens, placeholderTokens)

	limits.MaxInputTokens = placeholderTokens
	provider := NewOpenAIAssessorWithAssessmentLimits(newTestVerifierConfig("", "key", "assessor-model"), nil, limits)
	bounded, err := provider.boundRepairHistory(messages)
	require.NoError(t, err)
	require.Equal(t, semanticAssessmentRepairHistoryPlaceholder, bounded[2].Content)
}
