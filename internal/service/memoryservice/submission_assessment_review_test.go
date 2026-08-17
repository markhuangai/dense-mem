package memoryservice

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSubmissionAssessmentWorkerSupersedesQueuedOlderContractBeforeAssessment(t *testing.T) {
	ledger, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.placement.ContractVersion = "dense-mem.v2.4"

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	assert.True(t, processed)
	assert.Zero(t, provider.calls)
	assert.Empty(t, catalog.entityInputs)
	assert.Empty(t, assessments.commits)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), assessments.completions[0].Status)
	assert.Equal(t, "contract_superseded", assessments.completions[0].Payload["failure_stage"])
}

func TestSubmissionAssessmentWorkerHoldsWholeRunWhenOneSupportRangeIsLowConfidence(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		response := submissionAssessmentValidResponse(req, false)
		response.RelationshipResults[1].SupportRanges[0].Confidence = 0.4
		return response, nil
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, assessments.persistCalls)
	assert.Empty(t, assessments.commits)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, string(domain.SemanticReviewReviewRequired), assessments.completions[0].Status)
	assert.Equal(t, "policy_review", assessments.completions[0].Payload["failure_stage"])
	issues, ok := assessments.completions[0].Payload["hold_issues"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, issues, 1)
	assert.Equal(t, "grounding_low_confidence", issues[0]["code"])
	assert.Equal(t, "support", issues[0]["component"])
}

func TestSubmissionAssessmentReviewIssuesReportEveryHoldReason(t *testing.T) {
	groundingRef := "grounding-1"
	objectRef := "object"
	plan := submissionAssessmentPlan{RelationshipTargets: []submissionAssessmentRelationshipTarget{{
		Target: verifier.SemanticAssessmentRequiredRelationshipRef{
			ProposalID: "relationship-1",
			SubjectRef: "subject",
			ObjectRef:  &objectRef,
		},
	}}}
	response := verifier.SemanticAssessmentResponse{
		EntityResults: []verifier.SemanticAssessmentEntityResult{
			{Ref: "subject"},
			{Ref: "object", GroundingRef: &groundingRef, Action: string(domain.EntityResolutionAmbiguous), Confidence: 0.9},
		},
		RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{{
			Ref:             "relationship-1",
			Modality:        "proposal",
			PredicateStatus: "needs_review",
			ScopeStatus:     "needs_review",
			TemporalVerdict: "ambiguous",
			EvidenceVerdict: string(domain.VerificationContradicted),
			Confidence:      0.4,
			PredicateRange:  verifier.SemanticAssessmentGroundedRange{Confidence: 0.4},
			ValueRange:      &verifier.SemanticAssessmentGroundedRange{Confidence: 0.4},
			SupportRanges: []verifier.SemanticAssessmentGroundedRange{
				{Confidence: 0.4},
				{Confidence: 0.4},
			},
		}},
	}

	issues, truncated := submissionAssessmentReviewIssues(plan, response, 0.8)

	require.False(t, truncated)
	require.Len(t, issues, 11)
	issueKinds := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		issueKinds[issue.Code+":"+issue.Component] = struct{}{}
	}
	for _, expected := range []string{
		"entity_grounding_missing:subject",
		"entity_resolution_ambiguous:object",
		"unsupported_modality:relationship",
		"predicate_needs_review:predicate",
		"scope_needs_review:relationship",
		"temporal_uncertain:relationship",
		"evidence_not_entailed:support",
		"grounding_low_confidence:relationship",
		"grounding_low_confidence:predicate",
		"grounding_low_confidence:object",
		"grounding_low_confidence:support",
	} {
		assert.Contains(t, issueKinds, expected)
	}
}

func TestSubmissionAssessmentReviewIssuesHoldsLowConfidenceEntityGrounding(t *testing.T) {
	groundingRef := "grounding-1"
	objectRef := "object"
	plan := submissionAssessmentPlan{RelationshipTargets: []submissionAssessmentRelationshipTarget{{
		Target: verifier.SemanticAssessmentRequiredRelationshipRef{
			ProposalID: "relationship-1",
			SubjectRef: "subject",
			ObjectRef:  &objectRef,
		},
	}}}
	issues, truncated := submissionAssessmentReviewIssues(plan, verifier.SemanticAssessmentResponse{
		EntityResults: []verifier.SemanticAssessmentEntityResult{{
			Ref: "subject", GroundingRef: &groundingRef, Action: string(domain.EntityResolutionCreate), Confidence: 0.4,
		}},
	}, 0.8)

	require.False(t, truncated)
	require.Len(t, issues, 1)
	assert.Equal(t, "grounding_low_confidence", issues[0].Code)
	assert.Equal(t, "relationship-1", issues[0].RelationshipRef)
	assert.Equal(t, "subject", issues[0].Component)
}

func TestSubmissionAssessmentReviewIssuesAndCompletionAreBounded(t *testing.T) {
	results := make([]verifier.SemanticAssessmentRelationshipResult, 0, submissionAssessmentMaxHoldIssues+1)
	for index := 0; index <= submissionAssessmentMaxHoldIssues; index++ {
		results = append(results, verifier.SemanticAssessmentRelationshipResult{
			Ref:             fmt.Sprintf("relationship-%d", index),
			Modality:        "statement",
			PredicateStatus: "resolved",
			ScopeStatus:     "absent",
			TemporalVerdict: "absent",
			EvidenceVerdict: string(domain.VerificationEntailed),
			Confidence:      0.4,
			PredicateRange:  verifier.SemanticAssessmentGroundedRange{Confidence: 1},
			SupportRanges:   []verifier.SemanticAssessmentGroundedRange{{Confidence: 1}},
		})
	}
	issues, truncated := submissionAssessmentReviewIssues(
		submissionAssessmentPlan{},
		verifier.SemanticAssessmentResponse{RelationshipResults: results},
		0.8,
	)
	require.True(t, truncated)
	require.Len(t, issues, submissionAssessmentMaxHoldIssues)

	assessments := &submissionAssessmentWorkerAssessmentStub{}
	worker := &submissionAssessmentPlacementWorkerService{assessments: assessments}
	require.NoError(t, worker.completeReview(context.Background(), repository.SubmissionAssessmentRunScope{}, " policy_review ", nil, false))
	require.NoError(t, worker.completeReview(context.Background(), repository.SubmissionAssessmentRunScope{}, "policy_review", append(issues, issues[0]), false))
	require.Len(t, assessments.completions, 2)
	defaultIssues, ok := assessments.completions[0].Payload["hold_issues"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, defaultIssues, 1)
	assert.Equal(t, "commit_review_required", defaultIssues[0]["code"])
	boundedIssues, ok := assessments.completions[1].Payload["hold_issues"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, boundedIssues, submissionAssessmentMaxHoldIssues)
	assert.Equal(t, true, assessments.completions[1].Payload["hold_issues_truncated"])

	nilCompletion := &submissionAssessmentWorkerAssessmentStub{completeNil: true}
	err := (&submissionAssessmentPlacementWorkerService{assessments: nilCompletion}).completeReview(
		context.Background(),
		repository.SubmissionAssessmentRunScope{},
		"policy_review",
		issues,
		false,
	)
	require.ErrorContains(t, err, "nil review result")
	require.Equal(t, errSubmissionAssessmentRequiresReview.Error(), (&submissionAssessmentReviewRequiredError{}).Error())

	_, found := submissionAssessmentItemForFragment(submissionAssessmentPlan{}, "missing")
	require.False(t, found)
}
