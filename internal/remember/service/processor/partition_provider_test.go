package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
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
	for i, item := range input.Items {
		refs := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(item.BoundaryText, -1)
		response.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, EndRefs: []string{refs[len(refs)-1][1]}}
	}
	body, err := json.Marshal(response)
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func TestRememberPartitionRegeneratesCompleteResponsesAndRejectsExhaustion(t *testing.T) {
	provider := &processorAssessmentProviderStub{partitionReply: func(modelprovider.StructuredRequest) string { return `{"request_id":"foreign","items":[]}` }}
	p := &rememberSynchronousProcessor{provider: provider, limits: assessor.DefaultSemanticAssessmentLimits()}
	_, err := p.partitionRemember(t.Context(), remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "Exact meaning."}}}, "attempt")
	require.ErrorIs(t, err, remember.ErrRememberProviderResponseInvalid)
	require.Equal(t, 3, provider.partitionCalls)
}

func TestRememberPartitionRejectsInsufficientRegenerationHeadroomBeforeProvider(t *testing.T) {
	provider := &processorAssessmentProviderStub{}
	p := &rememberSynchronousProcessor{provider: provider, limits: assessor.SemanticAssessmentLimits{MaxInputTokens: 100, MaxOutputTokens: 8192}}
	_, err := p.partitionRemember(t.Context(), remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "Exact meaning."}}}, "attempt")
	require.ErrorIs(t, err, remember.ErrRememberInputBudgetExceeded)
	require.Zero(t, provider.partitionCalls)
}
