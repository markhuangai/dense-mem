package memoryservice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type submissionAssessmentCommitFixture struct {
	run        repository.PlacementRun
	scope      repository.SubmissionAssessmentRunScope
	plan       submissionAssessmentPlan
	request    verifier.SemanticAssessmentRequest
	response   verifier.SemanticAssessmentResponse
	assessment *repository.SubmissionAssessment
	policy     repository.AutoWriteConfidencePolicy
}

func TestSubmissionAssessmentCommitInputFailsClosedForUnsafeResults(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*submissionAssessmentCommitFixture)
		needsReview bool
	}{
		{
			name:   "missing persisted assessment",
			mutate: func(fixture *submissionAssessmentCommitFixture) { fixture.assessment = nil },
		},
		{
			name:   "invalid confidence threshold",
			mutate: func(fixture *submissionAssessmentCommitFixture) { fixture.policy.Threshold = 1.01 },
		},
		{
			name: "entity outside contract",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.EntityResults[0].Ref = "entity:unknown"
			},
		},
		{
			name: "ambiguous entity",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.EntityResults[0].Action = string(domain.EntityResolutionAmbiguous)
			},
			needsReview: true,
		},
		{
			name: "reuse without controlled candidate",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.EntityResults[0].Action = string(domain.EntityResolutionReuse)
				fixture.response.EntityResults[0].CandidateEntityID = nil
			},
			needsReview: true,
		},
		{
			name: "omitted relationship",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults = fixture.response.RelationshipResults[:len(fixture.response.RelationshipResults)-1]
			},
		},
		{
			name: "relationship outside contract",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].Ref = "relationship:unknown"
			},
		},
		{
			name: "review required relationship",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].PredicateStatus = "needs_review"
			},
			needsReview: true,
		},
		{
			name: "missing relationship support",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].Evidence = nil
			},
		},
		{
			name: "resolved predicate lacks versioned key",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].PredicateKey = nil
				fixture.response.RelationshipResults[0].PredicateVersion = nil
			},
			needsReview: true,
		},
		{
			name: "relationship references unresolved entity",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].SubjectRef = "entity:unknown"
			},
			needsReview: true,
		},
		{
			name: "relationship object is missing",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].ObjectRef = nil
				fixture.response.RelationshipResults[0].ObjectValue = nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := submissionAssessmentCommitInputFixture(t)
			test.mutate(&fixture)

			_, err := submissionAssessmentCommitInput(
				fixture.run,
				fixture.scope,
				fixture.plan,
				fixture.request,
				fixture.response,
				fixture.assessment,
				fixture.policy,
				false,
			)

			require.Error(t, err)
			if test.needsReview {
				assert.ErrorIs(t, err, errSubmissionAssessmentRequiresReview)
			}
		})
	}
}

func TestSubmissionAssessmentSupportsFailClosedForInvalidProvenance(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)

	for _, spans := range [][]verifier.SemanticAssessmentEvidenceSpan{
		nil,
		{{EvidenceID: "evidence:unknown", Start: 0, End: 1}},
		{{EvidenceID: fixture.plan.Items[0].EvidenceID, Start: 0, End: 999}},
	} {
		_, err := submissionAssessmentSupports(fixture.plan, fixture.assessment.AssessmentID, spans)
		require.Error(t, err)
	}

	fixture.plan.Items[0].Fragment.Authority = "unsupported"
	fixture.plan.itemsByEvidenceID[fixture.plan.Items[0].EvidenceID] = fixture.plan.Items[0]
	_, err := submissionAssessmentSupports(fixture.plan, fixture.assessment.AssessmentID, []verifier.SemanticAssessmentEvidenceSpan{{
		EvidenceID: fixture.plan.Items[0].EvidenceID, Start: 0, End: 5,
	}})
	require.Error(t, err)
}

func submissionAssessmentCommitInputFixture(t *testing.T) submissionAssessmentCommitFixture {
	t.Helper()
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	response, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, submissionAssessmentValidResponse(request, false), service.limits)
	require.Empty(t, validationErrors)
	return submissionAssessmentCommitFixture{
		run:      *ledger.run,
		scope:    submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker"),
		plan:     plan,
		request:  request,
		response: response,
		assessment: &repository.SubmissionAssessment{
			AssessmentID: uuid.NewString(),
			Model:        "submission-assessment-model",
			ResponseHash: "sha256:test",
			RequestID:    request.RequestID,
		},
		policy: assessments.policy,
	}
}
