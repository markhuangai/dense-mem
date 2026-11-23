//go:build integration

package serverapp

import (
	"encoding/json"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func partitionFixtureComplete(request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return partitionFixtureCompleteWithCuts(request, func(assessor.PartitionItem) []string { return []string{} })
}

func partitionFixtureCompleteWithCuts(request modelprovider.StructuredRequest, cuts func(assessor.PartitionItem) []string) (modelprovider.StructuredResult, error) {
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
		result.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, CutRefs: cuts(item)}
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
