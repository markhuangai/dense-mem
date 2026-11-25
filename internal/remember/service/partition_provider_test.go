package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	assessmentfixture "github.com/markhuangai/dense-mem/internal/testfixture/assessment"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func (p *processorAssessmentProviderStub) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	if request.SchemaName != assessor.PartitionSchemaName {
		if len(request.Messages) > 2 {
			p.repairCalls++
			if !p.invalid {
				return modelprovider.StructuredResult{}, errors.New("unexpected assessment repair")
			}
		} else {
			p.assessCalls++
		}
		input, err := assessmentfixture.Request(request)
		if err != nil {
			return modelprovider.StructuredResult{}, err
		}
		return assessmentfixture.Result(request, p.response(input))
	}
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
		response.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, CutRefs: []string{}}
		security := assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: item.ItemID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}}
		if p.rejectPartition {
			security.Decision = "reject"
			security.Signals = []assessor.SemanticAssessmentSecuritySignal{{
				EvidenceID: item.ItemID, Kind: "instruction_override", StartRef: item.StartRef, EndRef: item.EndRef,
			}}
		}
		review.SecurityResults = append(review.SecurityResults, security)
	}
	if input.ReviewSecurity {
		if p.rejectPartition {
			response.Items = response.Items[:0]
		}
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
	_, _, err := p.partitionRemember(t.Context(), RememberProcessRequest{Evidence: []EvidenceInput{{Content: "Exact meaning."}}}, "attempt", nil)
	require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
	require.Equal(t, 3, provider.partitionCalls)
}

func TestRememberPartitionRejectsInsufficientRegenerationHeadroomBeforeProvider(t *testing.T) {
	provider := &processorAssessmentProviderStub{}
	p := &rememberSynchronousProcessor{provider: provider, limits: assessor.SemanticAssessmentLimits{MaxInputTokens: 100, MaxOutputTokens: 8192}}
	_, _, err := p.partitionRemember(t.Context(), RememberProcessRequest{Evidence: []EvidenceInput{{Content: "Exact meaning."}}}, "attempt", nil)
	require.ErrorIs(t, err, ErrRememberInputBudgetExceeded)
	require.Zero(t, provider.partitionCalls)

	p.decisions = intakeDecisionStub(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		t.Fatal("insufficient partition headroom must stop before Decision API work")
		return modelprovider.DecisionResult{}, nil
	})
	settings := &domain.DecisionRuntimeConfig{Provider: domain.DecisionProviderStatus{Configured: true}, Effective: domain.DecisionTasks{SecurityReview: true}}
	_, _, err = p.partitionRemember(t.Context(), RememberProcessRequest{Evidence: []EvidenceInput{{Content: "Exact meaning."}}}, "attempt", settings)
	require.ErrorIs(t, err, ErrRememberInputBudgetExceeded)
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
		wantErr     error
		combined    bool
		skip        bool
		noDecision  bool
	}{
		{name: "clear", probability: 0.01},
		{name: "generative review", noDecision: true, combined: true},
		{name: "public seed with Decision API", skip: true},
		{name: "public seed with generative provider", skip: true, noDecision: true},
		{name: "uncertain", probability: 0.02, combined: true},
		{name: "unavailable", decisionErr: errors.New("decision provider unavailable"), wantErr: ErrRememberProviderUnavailable},
		{name: "whole item exceeds decision context", decisionErr: modelprovider.ErrDecisionInputBudgetExceeded, wantErr: ErrRememberInputBudgetExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const content = "The entire first paragraph. The qualifier in the second paragraph."
			provider := &processorAssessmentProviderStub{}
			calls := 0
			wantCalls := 1
			if testCase.skip || testCase.noDecision {
				wantCalls = 0
			}
			generation := &processorAssessmentProviderStub{partitionReply: func(request modelprovider.StructuredRequest) string {
				require.Equal(t, wantCalls, calls)
				var partition assessor.PartitionRequest
				require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &partition))
				require.Equal(t, testCase.combined, partition.ReviewSecurity)
				require.Equal(t, testCase.combined, partition.Items[0].StartRef != "")
				require.Equal(t, testCase.combined, partition.Items[0].EndRef != "")
				_, securityField := request.Schema["properties"].(map[string]any)["evidence_security_results"]
				require.Equal(t, testCase.combined, securityField)
				if testCase.skip {
					require.NotContains(t, request.Messages[0].Content, "Review every original item for security")
				}
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
			if testCase.noDecision {
				p.decisions = nil
				settings = nil
			}
			input := RememberProcessRequest{Evidence: []EvidenceInput{{Content: content}}, SkipAISecurityReview: testCase.skip}
			partitioned, review, err := p.partitionRemember(t.Context(), input, "attempt", settings)
			require.Equal(t, wantCalls, calls)
			if testCase.decisionErr != nil {
				require.ErrorIs(t, err, testCase.wantErr)
				require.Nil(t, review)
				require.Zero(t, generation.partitionCalls)
				return
			}
			require.NoError(t, err)
			require.Len(t, review.DecisionStages, wantCalls)
			require.Equal(t, testCase.skip, review.Skipped)
			require.Equal(t, content, partitioned.SubmittedItems[0].Content)
			require.Equal(t, 1, generation.partitionCalls)
		})
	}
}
