package memoryservice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSubmissionAssessmentWorkerSupersedesQueuedOlderContractBeforeAssessment(t *testing.T) {
	ledger, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.placement.ContractVersion = "dense-mem.v2.4"

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Zero(t, provider.calls)
	require.Empty(t, catalog.entityInputs)
	require.Empty(t, assessments.commits)
	require.Len(t, assessments.completions, 1)
	require.Equal(t, string(domain.SemanticReviewTerminalFailure), assessments.completions[0].Status)
	require.Equal(t, "contract_superseded", assessments.completions[0].Payload["failure_stage"])
	require.Equal(t, string(SubmissionErrorInternalFailure), assessments.completions[0].Payload["failure_code"])
}

func TestRepairSubmissionAssessmentResponseDoesNotInventGroundingOrIdentity(t *testing.T) {
	ledger, _, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	response := submissionAssessmentValidResponse(request, false)
	var repairedRef string
	for index := range response.EntityResults {
		result := &response.EntityResults[index]
		target := plan.entityTargetsByRef[result.Ref]
		if len(target.Target.Groundings) == 0 {
			continue
		}
		repairedRef = result.Ref
		result.GroundingRef = nil
		result.Action = string(domain.EntityResolutionAmbiguous)
		break
	}
	require.NotEmpty(t, repairedRef)

	unsupported := repairSubmissionAssessmentResponse(&plan, &response)

	require.Contains(t, unsupported, repairedRef)
	for _, result := range response.EntityResults {
		if result.Ref != repairedRef {
			continue
		}
		require.Nil(t, result.GroundingRef)
		require.Equal(t, string(domain.EntityResolutionAmbiguous), result.Action)
	}
	require.Equal(t, "submission-assessment-model", provider.ModelName())
}

func TestRepairSubmissionAssessmentResponseMarksUngroundableEntityUnsupported(t *testing.T) {
	plan := submissionAssessmentPlan{
		EntityTargets:      []submissionAssessmentEntityTarget{{Target: assessor.SemanticAssessmentRequiredEntityRef{Ref: "entity:missing"}}},
		entityTargetsByRef: map[string]submissionAssessmentEntityTarget{},
	}
	plan.entityTargetsByRef["entity:missing"] = plan.EntityTargets[0]
	response := assessor.SemanticAssessmentResponse{EntityResults: []assessor.SemanticAssessmentEntityResult{{Ref: "entity:missing"}}}

	unsupported := repairSubmissionAssessmentResponse(&plan, &response)

	require.Contains(t, unsupported, "entity:missing")
}
