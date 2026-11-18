package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

func (p *processorAssessmentProviderStub) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	p.partitionCalls++
	if p.partitionReply != nil {
		return modelprovider.StructuredResult{Content: p.partitionReply(request)}, nil
	}
	var input assessor.PartitionRequest
	if len(request.Messages) < 2 {
		return modelprovider.StructuredResult{}, fmt.Errorf("missing partition request")
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	response := assessor.PartitionResponse{RequestID: input.RequestID, Items: make([]assessor.PartitionResult, len(input.Items))}
	review := assessor.PartitionSecurityResponse{}
	for i, item := range input.Items {
		refs := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(item.BoundaryText, -1)
		response.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, EndRefs: []string{refs[len(refs)-1][1]}}
		review.SecurityResults = append(review.SecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: item.ItemID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
	}
	if input.ReviewSecurity {
		review.PartitionResponse = response
		body, err := json.Marshal(review)
		return modelprovider.StructuredResult{Content: string(body)}, err
	}
	body, err := json.Marshal(response)
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func TestRememberPartitionRegeneratesCompleteResponsesAndRejectsExhaustion(t *testing.T) {
	provider := &processorAssessmentProviderStub{partitionReply: func(modelprovider.StructuredRequest) string { return `{"request_id":"foreign","items":[]}` }}
	p := &rememberSynchronousProcessor{provider: provider, limits: assessor.DefaultSemanticAssessmentLimits()}
	_, _, err := p.partitionRemember(t.Context(), remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "Exact meaning."}}}, "attempt", nil)
	require.ErrorIs(t, err, remember.ErrRememberProviderResponseInvalid)
	require.Equal(t, 3, provider.partitionCalls)
}

func TestRememberPartitionRejectsInsufficientRegenerationHeadroomBeforeProvider(t *testing.T) {
	provider := &processorAssessmentProviderStub{}
	p := &rememberSynchronousProcessor{provider: provider, limits: assessor.SemanticAssessmentLimits{MaxInputTokens: 100, MaxOutputTokens: 8192}}
	_, _, err := p.partitionRemember(t.Context(), remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "Exact meaning."}}}, "attempt", nil)
	require.ErrorIs(t, err, remember.ErrRememberInputBudgetExceeded)
	require.Zero(t, provider.partitionCalls)
}

type intakeDecisionStub func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error)

func (f intakeDecisionStub) Decide(ctx context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return f(ctx, request)
}

func TestRememberPartitionUsesWholeDocumentDecisionBeforeGeneration(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		probability float64
		decisionErr error
		combined    bool
	}{
		{name: "clear", probability: 0.01},
		{name: "uncertain", probability: 0.02, combined: true},
		{name: "unavailable", decisionErr: errors.New("decision provider unavailable")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const content = "The entire first paragraph. The qualifier in the second paragraph."
			provider := &processorAssessmentProviderStub{}
			calls := 0
			generation := &processorAssessmentProviderStub{partitionReply: func(request modelprovider.StructuredRequest) string {
				require.Equal(t, 1, calls, "generation must follow the intake decision")
				var partition assessor.PartitionRequest
				require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &partition))
				require.Equal(t, testCase.combined, partition.ReviewSecurity)
				result, err := provider.Complete(t.Context(), request)
				require.NoError(t, err)
				return result.Content
			}}
			decisions := intakeDecisionStub(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				calls++
				require.Len(t, request.Groups, 1)
				require.Equal(t, content, request.Groups[0].Input.(map[string]any)["submitted_content"])
				if testCase.decisionErr != nil {
					return modelprovider.DecisionResult{}, testCase.decisionErr
				}
				answers := map[string]modelprovider.DecisionAnswer{}
				for _, question := range request.Groups[0].Questions {
					probability := testCase.probability
					answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
				}
				return modelprovider.DecisionResult{Answers: answers}, nil
			})
			settings := &domain.DecisionRuntimeConfig{Provider: domain.DecisionProviderStatus{Configured: true}, Effective: domain.DecisionTasks{SecurityReview: true, Deduplication: true}}
			p := &rememberSynchronousProcessor{provider: generation, decisions: decisions, limits: assessor.DefaultSemanticAssessmentLimits()}
			input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: content}}}
			partitioned, review, err := p.partitionRemember(t.Context(), input, "attempt", settings)
			require.Equal(t, 1, calls)
			if testCase.decisionErr != nil {
				require.ErrorIs(t, err, remember.ErrRememberProviderUnavailable)
				require.Nil(t, review)
				require.Zero(t, generation.partitionCalls)
				return
			}
			require.NoError(t, err)
			require.Len(t, review.DecisionStages, 1)
			require.Equal(t, content, partitioned.SubmittedItems[0].Content)
			require.Equal(t, 1, generation.partitionCalls)
		})
	}
}
