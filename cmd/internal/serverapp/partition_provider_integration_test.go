//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func partitionFixtureComplete(request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return partitionFixtureCompleteWithCuts(request, func(_ assessor.PartitionItem, refs []string) []string { return refs[len(refs)-1:] })
}

func partitionFixtureCompleteWithCuts(request modelprovider.StructuredRequest, cuts func(assessor.PartitionItem, []string) []string) (modelprovider.StructuredResult, error) {
	var input assessor.PartitionRequest
	if len(request.Messages) < 2 {
		return modelprovider.StructuredResult{}, fmt.Errorf("missing partition input")
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	result := assessor.PartitionResponse{RequestID: input.RequestID, Items: make([]assessor.PartitionResult, len(input.Items))}
	review := assessor.PartitionSecurityResponse{}
	for i, item := range input.Items {
		matches := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(item.BoundaryText, -1)
		refs := make([]string, len(matches))
		for j, match := range matches {
			refs[j] = match[1]
		}
		result.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, EndRefs: cuts(item, refs)}
		review.SecurityResults = append(review.SecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: item.ItemID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
	}
	if input.ReviewSecurity {
		review.PartitionResponse = result
		body, err := json.Marshal(review)
		return modelprovider.StructuredResult{Content: string(body)}, err
	}
	body, err := json.Marshal(result)
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func (*rememberProcessorIntegrationRegistrationAssessor) Complete(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return partitionFixtureComplete(r)
}
func (rememberProcessorIntegrationAssessor) Complete(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return partitionFixtureComplete(r)
}
func (*importAdmissionAssessor) Complete(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return partitionFixtureComplete(r)
}
