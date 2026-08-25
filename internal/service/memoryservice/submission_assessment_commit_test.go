package memoryservice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type submissionAssessmentCommitFixture struct {
	run        repository.PlacementRun
	scope      repository.SubmissionAssessmentRunScope
	plan       submissionAssessmentPlan
	request    assessor.SemanticAssessmentRequest
	response   assessor.SemanticAssessmentResponse
	assessment *repository.SubmissionAssessment
}

func TestSubmissionAssessmentCommitInputFailsClosedForUnsafeResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*submissionAssessmentCommitFixture)
	}{
		{
			name:   "missing persisted assessment",
			mutate: func(fixture *submissionAssessmentCommitFixture) { fixture.assessment = nil },
		},
		{
			name: "entity outside contract",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.EntityResults[0].Ref = "entity:unknown"
			},
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
			name: "missing relationship support",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].Splits[0].Evidence = nil
			},
		},
		{
			name: "resolved predicate lacks versioned key",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].Splits[0].PredicateKey = nil
				fixture.response.RelationshipResults[0].Splits[0].PredicateVersion = nil
			},
		},
		{
			name: "relationship object is missing",
			mutate: func(fixture *submissionAssessmentCommitFixture) {
				fixture.response.RelationshipResults[0].Splits[0].ObjectRef = nil
				fixture.response.RelationshipResults[0].Splits[0].ObjectValue = nil
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
				fixture.response,
				fixture.assessment,
				false,
			)

			require.Error(t, err)
		})
	}
}

func TestSubmissionAssessmentCommitInputCanonicalizesRelationshipRefsAfterEntityGroundingDeduplication(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)
	var canonical, duplicate *assessor.SemanticAssessmentEntityResult
	for index := range fixture.response.EntityResults {
		result := &fixture.response.EntityResults[index]
		switch result.Ref {
		case "entity:0:subject":
			canonical = result
		case "entity:1:subject":
			duplicate = result
		}
	}
	require.NotNil(t, canonical)
	require.NotNil(t, duplicate)
	duplicate.EvidenceID = canonical.EvidenceID
	duplicate.Start = canonical.Start
	duplicate.End = canonical.End

	commit, err := submissionAssessmentCommitInput(
		fixture.run,
		fixture.scope,
		fixture.plan,
		fixture.response,
		fixture.assessment,
		false,
	)

	require.NoError(t, err)
	var dependsOn repository.PlacementRelationshipDecisionInput
	for _, observation := range commit.RelationshipObservations {
		if observation.Observation.Ref == "r:depends" {
			dependsOn = observation.Observation
			break
		}
	}
	require.NotEmpty(t, dependsOn.Ref)
	assert.Equal(t, "entity:0:subject", dependsOn.SubjectRef)
}

func TestSubmissionAssessmentCommitInputDoesNotManufactureLegacyAssessmentDefaults(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)

	commit, err := submissionAssessmentCommitInput(
		fixture.run,
		fixture.scope,
		fixture.plan,
		fixture.response,
		fixture.assessment,
		false,
	)
	require.NoError(t, err)
	require.NotEmpty(t, commit.RelationshipObservations)
	for _, entry := range commit.RelationshipObservations {
		observation := entry.Observation
		assert.True(t, observation.AssessorAccepted)
		assert.Empty(t, observation.EvidenceVerdict)
		assert.Nil(t, observation.Confidence)
		assert.Empty(t, observation.Rationale)
		assert.Empty(t, observation.AssessmentPolicyVersion)
		assert.Nil(t, observation.ThresholdUsed)
		assert.Empty(t, observation.GateResult)
	}
}

func TestSubmissionAssessmentCommitInputCarriesExactConstraintsIntoAtomicCommit(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)
	entityResult := &fixture.response.EntityResults[0]
	knownEntityID := uuid.NewString()
	entityResult.Action = "reuse"
	entityResult.CandidateEntityID = &knownEntityID
	entityTarget := fixture.plan.entityTargetsByRef[entityResult.Ref]
	entityTarget.KnownEntityID = knownEntityID
	fixture.plan.entityTargetsByRef[entityResult.Ref] = entityTarget
	relationshipTarget := fixture.plan.relationshipsByRef["r:uses"]
	predicateKey := relationshipTarget.Target.PredicateHint
	for _, result := range fixture.response.RelationshipResults {
		if result.Ref == "r:uses" && len(result.Splits) > 0 && result.Splits[0].PredicateKey != nil {
			predicateKey = *result.Splits[0].PredicateKey
		}
	}
	relationshipTarget.KnownPredicateKey = predicateKey
	fixture.plan.relationshipsByRef["r:uses"] = relationshipTarget

	commit, err := submissionAssessmentCommitInput(
		fixture.run, fixture.scope, fixture.plan, fixture.response, fixture.assessment, false,
	)
	require.NoError(t, err)

	foundEntity := false
	for _, entry := range commit.EntityResolutions {
		if entry.Resolution.MentionRef == entityResult.Ref {
			assert.Equal(t, knownEntityID, entry.Resolution.ExactEntityID)
			foundEntity = true
		}
	}
	require.True(t, foundEntity)
	foundRelationship := false
	for _, entry := range commit.RelationshipObservations {
		if entry.RelationshipRef == "r:uses" {
			assert.Equal(t, predicateKey, entry.Observation.ExactPredicateKey)
			foundRelationship = true
		}
	}
	require.True(t, foundRelationship)
}

func TestSubmissionAssessmentCommitInputRejectsStoredUngroundedRelationship(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)
	var betaTarget *submissionAssessmentEntityTarget
	for index := range fixture.plan.EntityTargets {
		if fixture.plan.EntityTargets[index].Target.Ref == "entity:0:object" {
			betaTarget = &fixture.plan.EntityTargets[index]
			break
		}
	}
	require.NotNil(t, betaTarget)
	betaTarget.Target.Groundings = nil
	fixture.plan.entityTargetsByRef[betaTarget.Target.Ref] = *betaTarget
	for index := range fixture.response.EntityResults {
		if fixture.response.EntityResults[index].Ref == betaTarget.Target.Ref {
			fixture.response.EntityResults[index].GroundingRef = nil
			break
		}
	}

	_, err := submissionAssessmentCommitInput(
		fixture.run,
		fixture.scope,
		fixture.plan,
		fixture.response,
		fixture.assessment,
		false,
	)

	require.ErrorContains(t, err, "stored split references an ungrounded Entity")
}

func TestSubmissionAssessmentCommitInputPreservesMultipleSplits(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)
	var target *assessor.SemanticAssessmentRelationshipResult
	for index := range fixture.response.RelationshipResults {
		if fixture.response.RelationshipResults[index].Ref == "r:uses" {
			target = &fixture.response.RelationshipResults[index]
			break
		}
	}
	require.NotNil(t, target)
	second := target.Splits[0]
	second.SplitIndex = 1
	target.Splits = append(target.Splits, second)

	commit, err := submissionAssessmentCommitInput(
		fixture.run,
		fixture.scope,
		fixture.plan,
		fixture.response,
		fixture.assessment,
		false,
	)
	require.NoError(t, err)

	var splits []repository.SubmissionAssessmentRelationshipObservationInput
	for _, observation := range commit.RelationshipObservations {
		if observation.RelationshipRef == "r:uses" {
			splits = append(splits, observation)
		}
	}
	require.Len(t, splits, 2)
	assert.Equal(t, 0, splits[0].SplitIndex)
	assert.Equal(t, 1, splits[1].SplitIndex)
	assert.NotEqual(t, splits[0].Observation.Ref, splits[1].Observation.Ref)
}

func TestSubmissionAssessmentSupportsFailClosedForInvalidProvenance(t *testing.T) {
	fixture := submissionAssessmentCommitInputFixture(t)

	for _, spans := range [][]assessor.SemanticAssessmentEvidenceSpan{
		nil,
		{{EvidenceID: "evidence:unknown", Start: 0, End: 1}},
		{{EvidenceID: fixture.plan.Items[0].EvidenceID, Start: 0, End: 999}},
	} {
		_, err := submissionAssessmentSupports(fixture.plan, fixture.assessment.AssessmentID, spans)
		require.Error(t, err)
	}

	fixture.plan.Items[0].Fragment.Authority = "unsupported"
	fixture.plan.itemsByEvidenceID[fixture.plan.Items[0].EvidenceID] = fixture.plan.Items[0]
	_, err := submissionAssessmentSupports(fixture.plan, fixture.assessment.AssessmentID, []assessor.SemanticAssessmentEvidenceSpan{{
		EvidenceID: fixture.plan.Items[0].EvidenceID, Start: 0, End: 5,
	}})
	require.Error(t, err)
}

func submissionAssessmentCommitInputFixture(t *testing.T) submissionAssessmentCommitFixture {
	t.Helper()
	ledger, _, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	response, validationErrors := assessor.PrepareSemanticAssessmentResponse(request, submissionAssessmentValidResponse(request, false), service.limits)
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
	}
}
