//go:build integration

package ontology

import (
	"context"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

// AssessDiagnostic uses native construction and validation without deterministic shortcuts or publication, only in integration builds.
func (s *Service) AssessDiagnostic(ctx context.Context, sourceContext contract.OrganizationContext) (assessment.Request, assessment.Response, []contract.AssessmentAttempt, error) {
	receipt := contract.OrganizationReceipt{ID: uuid.NewString()}
	for _, source := range sourceContext.Sources {
		receipt.Result.Outcomes = append(receipt.Result.Outcomes, contract.OrganizationOutcome{Source: source.SourceHandle, Status: "unchanged"})
	}
	request, _, err := s.request(sourceContext, &receipt)
	if err != nil {
		return request, assessment.Response{}, nil, err
	}
	response, attempts, err := s.provider.Assess(ctx, request)
	return request, response, attempts, err
}
