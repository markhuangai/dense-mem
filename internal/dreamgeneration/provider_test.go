package dreamgeneration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestGraphProviderPreservesProviderUsageAccounting(t *testing.T) {
	transport := &graphProviderTransportStub{}
	provider := NewProvider(transport, "graph-model", DefaultSemanticAssessmentLimits())
	response, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.NoError(t, err)
	require.Equal(t, 17, response.InputTokens)
	require.Equal(t, 23, response.OutputTokens)
	require.Equal(t, 1, response.ProviderTurns)
	require.Len(t, transport.requests, 1)
}

func TestProviderHandlesUnavailableTransportAndModelName(t *testing.T) {
	var nilProvider *Provider
	require.Empty(t, nilProvider.ModelName())
	_, err := nilProvider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.Error(t, err)
	_, err = nilProvider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.Error(t, err)

	provider := NewProvider(&errorStructuredTransport{}, "model", DefaultSemanticAssessmentLimits())
	require.Equal(t, "model", provider.ModelName())
	_, err = provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.ErrorContains(t, err, "transport failed")
	_, err = provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.ErrorContains(t, err, "transport failed")
}

func TestGraphProviderRepairsMalformedResponseWithBoundedCorrection(t *testing.T) {
	transport := &graphCorrectionTransportStub{}
	provider := NewProvider(transport, "graph-model", DefaultSemanticAssessmentLimits())
	response, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.NoError(t, err)
	require.Empty(t, response.Proposals)
	require.Equal(t, 2, response.ProviderTurns)
	require.Len(t, transport.requests, 2)
	require.Contains(t, transport.requests[1].Messages[3].Content, "complete replacement JSON object")
}

func TestProviderRejectsInvalidRequestsAndTokenBudgets(t *testing.T) {
	provider := NewProvider(&graphProviderTransportStub{}, "graph-model", DefaultSemanticAssessmentLimits())
	_, err := provider.GenerateDreams(context.Background(), DreamGenerationRequest{})
	var providerErr *modelprovider.ProviderError
	require.ErrorAs(t, err, &providerErr)
	require.Equal(t, modelprovider.ProviderFailureClassRequestInvalid, providerErr.FailureClass)
	_, err = provider.GenerateEvidenceDiscoveries(context.Background(), EvidenceDiscoveryRequest{})
	require.ErrorAs(t, err, &providerErr)
	require.Equal(t, modelprovider.ProviderFailureClassRequestInvalid, providerErr.FailureClass)

	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	budgeted := NewProvider(&graphProviderTransportStub{}, "graph-model", limits)
	_, err = budgeted.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.ErrorAs(t, err, &providerErr)
	require.Equal(t, modelprovider.ProviderFailureClassRequestInvalid, providerErr.FailureClass)

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1
	outputBudgeted := NewProvider(&graphProviderTransportStub{}, "graph-model", limits)
	_, err = outputBudgeted.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.Error(t, err)
}

func TestProviderCorrectionErrorFormattingIsBounded(t *testing.T) {
	input := []assessor.SemanticValidationError{{Field: strings.Repeat("f", 200), Message: strings.Repeat("m", 400)}}
	correction := boundedCorrectionErrors(input)
	require.Len(t, correction, 1)
	require.Len(t, correction[0]["field"], 128)
	require.Len(t, correction[0]["message"], 256)
	graphCorrection := boundedGraphCorrectionErrors(input)
	require.Len(t, graphCorrection, 1)
}

func TestProviderRejectsReportedUsageAndTokenizerFailures(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 10_000
	graphUsage := &usageStructuredTransport{promptTokens: limits.MaxInputTokens + 1}
	provider := NewProvider(graphUsage, "model", limits)
	_, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	var malformed *modelprovider.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "input_budget", malformed.FailureClass)
	require.Equal(t, 1, malformed.Attempts)
	require.Equal(t, 1, graphUsage.calls)

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 100
	provider = NewProvider(&usageStructuredTransport{completionTokens: limits.MaxOutputTokens + 1}, "model", limits)
	_, err = provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.Error(t, err)

	limits = DefaultSemanticAssessmentLimits()
	limits.Tokenizer = "invalid-tokenizer"
	provider = NewProvider(&graphProviderTransportStub{}, "model", limits)
	_, err = provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.Error(t, err)
}

func TestProviderRejectsTheOppositeReportedUsageOverages(t *testing.T) {
	var malformed *modelprovider.MalformedResponseError
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 100
	provider := NewProvider(&usageStructuredTransport{completionTokens: limits.MaxOutputTokens + 1}, "model", limits)
	_, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.Error(t, err)

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 10_000
	evidenceUsage := &usageStructuredTransport{promptTokens: limits.MaxInputTokens + 1}
	provider = NewProvider(evidenceUsage, "model", limits)
	_, err = provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "input_budget", malformed.FailureClass)
	require.Equal(t, 1, malformed.Attempts)
	require.Equal(t, 1, evidenceUsage.calls)
}

func TestProviderRepairsMalformedJSONBeforeAcceptingACompleteResponse(t *testing.T) {
	transport := &invalidJSONThenValidTransport{}
	provider := NewProvider(transport, "model", DefaultSemanticAssessmentLimits())
	response, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.NoError(t, err)
	require.Empty(t, response.Proposals)
	require.Equal(t, 2, response.ProviderTurns)

	transport = &invalidJSONThenValidTransport{}
	provider = NewProvider(transport, "model", DefaultSemanticAssessmentLimits())
	evidenceResponse, err := provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.NoError(t, err)
	require.Empty(t, evidenceResponse.Proposals)
	require.Equal(t, 2, evidenceResponse.ProviderTurns)
}

func TestProviderStopsBeforeARepairedRequestExceedsTheInputBudget(t *testing.T) {
	t.Run("graph", func(t *testing.T) {
		req := dreamGenerationTestRequest(t)
		limits := DefaultSemanticAssessmentLimits()
		prepared, errs := PrepareDreamGenerationRequest(req, limits)
		require.Empty(t, errs)
		payload, err := json.Marshal(prepared)
		require.NoError(t, err)
		messages := []modelprovider.Message{{Role: "system", Content: dreamGenerationSystemPrompt}, {Role: "user", Content: string(payload)}}
		budget, err := graphMessageTokens(messages, limits.Tokenizer)
		require.NoError(t, err)
		requestBudget, err := assessor.CountTokens(dreamGenerationSystemPrompt+string(payload), limits.Tokenizer)
		require.NoError(t, err)
		if budget < requestBudget {
			budget = requestBudget
		}
		limits.MaxInputTokens = budget
		provider := NewProvider(&invalidJSONThenValidTransport{}, "model", limits)
		_, err = provider.GenerateDreams(context.Background(), req)
		var malformed *modelprovider.MalformedResponseError
		require.ErrorAs(t, err, &malformed)
		require.Equal(t, "input_budget", malformed.FailureClass)
		require.Equal(t, 1, malformed.Attempts)
	})

	t.Run("evidence", func(t *testing.T) {
		req := evidenceDiscoveryTestRequest(t)
		limits := DefaultSemanticAssessmentLimits()
		prepared, errs := PrepareEvidenceDiscoveryRequest(req, limits)
		require.Empty(t, errs)
		payload, err := json.Marshal(prepared)
		require.NoError(t, err)
		messages := []modelprovider.Message{{Role: "system", Content: evidenceDiscoverySystemPrompt}, {Role: "user", Content: string(payload)}}
		budget, err := messageTokens(messages, limits.Tokenizer)
		require.NoError(t, err)
		requestBudget, err := assessor.CountTokens(evidenceDiscoverySystemPrompt+string(payload), limits.Tokenizer)
		require.NoError(t, err)
		if budget < requestBudget {
			budget = requestBudget
		}
		limits.MaxInputTokens = budget
		provider := NewProvider(&invalidJSONThenValidTransport{}, "model", limits)
		_, err = provider.GenerateEvidenceDiscoveries(context.Background(), req)
		var malformed *modelprovider.MalformedResponseError
		require.ErrorAs(t, err, &malformed)
		require.Equal(t, "input_budget", malformed.FailureClass)
		require.Equal(t, 1, malformed.Attempts)
	})
}

func TestProviderReportsInvalidProviderOutputEncoding(t *testing.T) {
	provider := NewProvider(invalidUTF8Transport{}, "model", DefaultSemanticAssessmentLimits())
	_, err := provider.GenerateDreams(context.Background(), dreamGenerationTestRequest(t))
	require.Error(t, err)
	_, err = provider.GenerateEvidenceDiscoveries(context.Background(), evidenceDiscoveryTestRequest(t))
	require.Error(t, err)
}

func TestProviderCorrectionErrorsAreSortedAndBounded(t *testing.T) {
	errs := make([]assessor.SemanticValidationError, assessor.SemanticAssessmentMaxCorrectionErrors+2)
	for index := range errs {
		errs[index] = assessor.SemanticValidationError{Field: strings.Repeat("f", index%3+1), Message: strings.Repeat("m", index+1)}
	}
	bounded := boundedGraphCorrectionErrors(errs)
	require.Len(t, bounded, assessor.SemanticAssessmentMaxCorrectionErrors)
	require.Equal(t, "response", bounded[len(bounded)-1].Field)
	require.Contains(t, bounded[len(bounded)-1].Message, "additional validation errors")

	correction := boundedCorrectionErrors(make([]assessor.SemanticValidationError, 34))
	require.Len(t, correction, 32)
}

func TestDreamGenerationLimitsNormalizeUnsetValues(t *testing.T) {
	defaults := DefaultSemanticAssessmentLimits()
	normalized := normalizeLimits(SemanticAssessmentLimits{})
	require.Equal(t, defaults.Tokenizer, normalized.Tokenizer)
	require.Equal(t, defaults.MaxInputTokens, normalized.MaxInputTokens)
	require.Equal(t, defaults.MaxOutputTokens, normalized.MaxOutputTokens)
}

type graphProviderTransportStub struct {
	requests []modelprovider.StructuredRequest
}

type errorStructuredTransport struct{}

func (errorStructuredTransport) Complete(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return modelprovider.StructuredResult{}, errors.New("transport failed")
}

type usageStructuredTransport struct {
	promptTokens     int
	completionTokens int
	calls            int
}

type invalidJSONThenValidTransport struct {
	requests []modelprovider.StructuredRequest
}

func (s *invalidJSONThenValidTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	s.requests = append(s.requests, request)
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if len(s.requests) == 1 {
		return modelprovider.StructuredResult{Content: "{"}, nil
	}
	return modelprovider.StructuredResult{Content: validEvidenceDiscoveryResponseJSON(payload.RequestID)}, nil
}

type invalidUTF8Transport struct{}

func (invalidUTF8Transport) Complete(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return modelprovider.StructuredResult{Content: string([]byte{0xff})}, nil
}

func (s *usageStructuredTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	s.calls++
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	content := `{"request_id":"` + payload.RequestID + `","proposals":[]}`
	return modelprovider.StructuredResult{Content: content, PromptTokens: s.promptTokens, CompletionTokens: s.completionTokens}, nil
}

type graphCorrectionTransportStub struct {
	requests []modelprovider.StructuredRequest
}

func (s *graphCorrectionTransportStub) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	s.requests = append(s.requests, request)
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if len(s.requests) == 1 {
		return modelprovider.StructuredResult{Content: `{"request_id":"` + payload.RequestID + `","proposals":[{"path_ref":"missing","predicate_ref":"missing","statement":"x","rationale":"x","what_if":"x","possible_outcome":"x","likelihood":0.5,"confidence":0.5,"evidence_refs":[]}]}`}, nil
	}
	return modelprovider.StructuredResult{Content: `{"request_id":"` + payload.RequestID + `","proposals":[]}`}, nil
}

func (s *graphProviderTransportStub) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	s.requests = append(s.requests, request)
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	return modelprovider.StructuredResult{
		Content:          `{"request_id":"` + payload.RequestID + `","proposals":[]}`,
		PromptTokens:     17,
		CompletionTokens: 23,
		TotalTokens:      40,
	}, nil
}
