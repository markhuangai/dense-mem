package memoryservice

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionAssessmentCommitInputRejectsUnsafeResults(t *testing.T) {
	prepared, scope := preparedCommitAssessment(t)
	_, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, nil, false)
	require.ErrorContains(t, err, "persisted submission assessment is required")

	cases := []struct {
		name   string
		mutate func(*SynchronousAssessmentResult)
		want   string
	}{
		{
			name: "entity outside contract",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EntityResults[0].Ref = "entity:unknown"
			},
			want: "entity result is outside the contract",
		},
		{
			name: "entity grounding outside run",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EntityResults[0].EvidenceID = "evidence:missing"
			},
			want: "entity grounding is outside the run",
		},
		{
			name: "exact entity cannot be created",
			mutate: func(prepared *SynchronousAssessmentResult) {
				result := &prepared.Response.EntityResults[0]
				target := prepared.Plan.entityTargetsByRef[result.Ref]
				target.KnownEntityID = uuid.NewString()
				prepared.Plan.entityTargetsByRef[result.Ref] = target
				result.Action = string(domain.EntityResolutionCreate)
			},
			want: "changed exact entity constraint",
		},
		{
			name: "reuse without candidate creates entity",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EntityResults[0].CandidateEntityID = nil
			},
			want: "",
		},
		{
			name: "reuse exact entity fills candidate",
			mutate: func(prepared *SynchronousAssessmentResult) {
				result := &prepared.Response.EntityResults[0]
				target := prepared.Plan.entityTargetsByRef[result.Ref]
				target.KnownEntityID = uuid.NewString()
				prepared.Plan.entityTargetsByRef[result.Ref] = target
				result.CandidateEntityID = nil
			},
			want: "",
		},
		{
			name: "reuse exact entity changes candidate",
			mutate: func(prepared *SynchronousAssessmentResult) {
				result := &prepared.Response.EntityResults[0]
				target := prepared.Plan.entityTargetsByRef[result.Ref]
				target.KnownEntityID = uuid.NewString()
				prepared.Plan.entityTargetsByRef[result.Ref] = target
				candidate := uuid.NewString()
				result.CandidateEntityID = &candidate
			},
			want: "changed exact entity constraint",
		},
		{
			name: "unsupported entity action",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EntityResults[0].Action = "unsupported"
			},
			want: "unsupported entity action",
		},
		{
			name: "conflicting duplicate grounding",
			mutate: func(prepared *SynchronousAssessmentResult) {
				first := prepared.Response.EntityResults[0]
				second := &prepared.Response.EntityResults[1]
				second.EvidenceID, second.Start, second.End, second.Kind = first.EvidenceID, first.Start, first.End, first.Kind
				first.Action = string(domain.EntityResolutionReuse)
				firstCandidate := uuid.NewString()
				first.CandidateEntityID = &firstCandidate
				second.Action = first.Action
				secondCandidate := uuid.NewString()
				second.CandidateEntityID = &secondCandidate
				second.Surface = first.Surface
			},
			want: "conflicting entity groundings",
		},
		{
			name: "omitted entity",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.EntityResults = prepared.Response.EntityResults[:1]
			},
			want: "omitted an entity result",
		},
		{
			name: "relationship outside contract",
			mutate: func(prepared *SynchronousAssessmentResult) {
				relationship := firstCommitRelationship(&prepared.Response)
				relationship.Ref = "relationship:unknown"
			},
			want: "relationship result is outside the contract",
		},
		{
			name: "duplicate relationship",
			mutate: func(prepared *SynchronousAssessmentResult) {
				prepared.Response.RelationshipResults = append(prepared.Response.RelationshipResults, prepared.Response.RelationshipResults[0])
			},
			want: "duplicate relationship result",
		},
		{
			name: "unsupported relationship disposition",
			mutate: func(prepared *SynchronousAssessmentResult) {
				firstCommitRelationship(&prepared.Response).Disposition = "unsupported"
			},
			want: "unsupported relationship disposition",
		},
		{
			name: "stored relationship has no split",
			mutate: func(prepared *SynchronousAssessmentResult) {
				firstCommitRelationship(&prepared.Response).Splits = nil
			},
			want: "stored relationship has no split",
		},
		{
			name: "exact lifecycle cannot split",
			mutate: func(prepared *SynchronousAssessmentResult) {
				relationship := firstCommitRelationship(&prepared.Response)
				target := prepared.Plan.relationshipsByRef[relationship.Ref]
				target.CorrectionTarget = &repository.SemanticCorrectionTargetInput{RelationshipID: uuid.NewString(), ExpectedVersion: 1}
				prepared.Plan.relationshipsByRef[relationship.Ref] = target
				second := relationship.Splits[0]
				second.SplitIndex = 1
				relationship.Splits = append(relationship.Splits, second)
			},
			want: "split an exact lifecycle operation",
		},
		{
			name: "ungrounded entity in split",
			mutate: func(prepared *SynchronousAssessmentResult) {
				split := &firstCommitRelationship(&prepared.Response).Splits[0]
				for index := range prepared.Response.EntityResults {
					if prepared.Response.EntityResults[index].Ref == split.SubjectRef {
						prepared.Response.EntityResults[index].Action = string(domain.EntityResolutionAmbiguous)
						prepared.Response.EntityResults[index].GroundingRef = nil
					}
				}
			},
			want: "references an ungrounded Entity",
		},
		{
			name: "unsupported predicate status",
			mutate: func(prepared *SynchronousAssessmentResult) {
				firstCommitRelationship(&prepared.Response).Splits[0].PredicateStatus = "unsupported"
			},
			want: "unsupported predicate status",
		},
		{
			name: "invalid validity",
			mutate: func(prepared *SynchronousAssessmentResult) {
				invalid := "not-a-time"
				firstCommitRelationship(&prepared.Response).Splits[0].ValidFrom = &invalid
			},
			want: "validity is invalid",
		},
		{
			name: "support outside run",
			mutate: func(prepared *SynchronousAssessmentResult) {
				firstCommitRelationship(&prepared.Response).Splits[0].Evidence[0].EvidenceID = "evidence:missing"
			},
			want: "evidence span is outside the run",
		},
		{
			name: "support fragment outside run",
			mutate: func(prepared *SynchronousAssessmentResult) {
				for index := range prepared.Plan.Items {
					prepared.Plan.Items[index].Fragment.FragmentID = "outside-fragment"
				}
			},
			want: "support is outside the run",
		},
		{
			name: "relationship object missing",
			mutate: func(prepared *SynchronousAssessmentResult) {
				split := &firstCommitRelationship(&prepared.Response).Splits[0]
				split.ObjectRef, split.ObjectValue = nil, nil
			},
			want: "object is missing",
		},
		{
			name: "resolved predicate incomplete",
			mutate: func(prepared *SynchronousAssessmentResult) {
				split := &firstCommitRelationship(&prepared.Response).Splits[0]
				split.PredicateKey, split.PredicateVersion = nil, nil
			},
			want: "resolved predicate is incomplete",
		},
		{
			name: "exact predicate changes",
			mutate: func(prepared *SynchronousAssessmentResult) {
				relationship := firstCommitRelationship(&prepared.Response)
				target := prepared.Plan.relationshipsByRef[relationship.Ref]
				target.KnownPredicateKey = "exact-predicate"
				prepared.Plan.relationshipsByRef[relationship.Ref] = target
				changed := "different-predicate"
				relationship.Splits[0].PredicateKey = &changed
			},
			want: "changed exact predicate constraint",
		},
		{
			name: "registration missing",
			mutate: func(prepared *SynchronousAssessmentResult) {
				split := &firstCommitRelationship(&prepared.Response).Splits[0]
				split.PredicateStatus = "registration_required"
				split.PredicateRegistration = nil
			},
			want: "predicate registration is incomplete",
		},
		{
			name: "registration exact predicate is stale",
			mutate: func(prepared *SynchronousAssessmentResult) {
				relationship := firstCommitRelationship(&prepared.Response)
				target := prepared.Plan.relationshipsByRef[relationship.Ref]
				target.KnownPredicateKey = "exact-predicate"
				prepared.Plan.relationshipsByRef[relationship.Ref] = target
				split := &relationship.Splits[0]
				split.PredicateStatus = "registration_required"
				split.PredicateRegistration = &assessor.SemanticAssessmentPredicateRegistration{PredicateKey: "new-predicate"}
			},
			want: "could not preserve exact predicate constraint",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			prepared, scope := preparedCommitAssessment(t)
			test.mutate(prepared)
			commit, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, false)
			if test.want == "" {
				require.NoError(t, err)
				require.NotEmpty(t, commit.EntityResolutions)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestSubmissionAssessmentCommitInputCarriesAliasesConstraintsAndRegistrations(t *testing.T) {
	prepared, scope := preparedCommitAssessment(t)
	firstEntity := prepared.Response.EntityResults[0]
	secondEntity := &prepared.Response.EntityResults[1]
	secondEntity.EvidenceID, secondEntity.Start, secondEntity.End, secondEntity.Kind = firstEntity.EvidenceID, firstEntity.Start, firstEntity.End, firstEntity.Kind
	secondEntity.Action = firstEntity.Action
	secondEntity.CandidateEntityID = firstEntity.CandidateEntityID
	secondEntity.Surface = firstEntity.Surface

	relationship := firstCommitRelationship(&prepared.Response)
	target := prepared.Plan.relationshipsByRef[relationship.Ref]
	target.KnownPredicateKey = *relationship.Splits[0].PredicateKey
	target.CorrectionTarget = &repository.SemanticCorrectionTargetInput{RelationshipID: uuid.NewString(), ExpectedVersion: 2}
	target.ConflictContext = &repository.SemanticConflictContextInput{ConflictID: uuid.NewString(), ExpectedVersion: 3}
	prepared.Plan.relationshipsByRef[relationship.Ref] = target

	commit, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, true)
	require.NoError(t, err)
	require.NotEmpty(t, commit.EntityResolutions)
	require.NotEmpty(t, commit.RelationshipObservations)
	require.Equal(t, target.KnownPredicateKey, commit.RelationshipObservations[0].Observation.ExactPredicateKey)
	require.NotNil(t, commit.RelationshipObservations[0].Observation.CorrectionTarget)
	require.NotNil(t, commit.RelationshipObservations[0].Observation.ConflictContext)
	require.Equal(t, true, commit.Payload["assessment_reused"])
}

func TestSubmissionAssessmentCommitInputRestoresRelationshipTargetOrder(t *testing.T) {
	prepared, scope := preparedCommitAssessment(t)
	for left, right := 0, len(prepared.Response.RelationshipResults)-1; left < right; left, right = left+1, right-1 {
		prepared.Response.RelationshipResults[left], prepared.Response.RelationshipResults[right] = prepared.Response.RelationshipResults[right], prepared.Response.RelationshipResults[left]
	}

	commit, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, false)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipResults, len(prepared.Plan.RelationshipTargets))
	for index, target := range prepared.Plan.RelationshipTargets {
		require.Equal(t, target.Target.ProposalID, commit.RelationshipResults[index].RelationshipRef)
	}
}

func TestSubmissionAssessmentCommitInputReturnsNotStoredWarningsForUnsupportedRelationships(t *testing.T) {
	prepared, scope := preparedCommitAssessment(t)
	for index := range prepared.Response.RelationshipResults {
		if index == 0 {
			prepared.Response.RelationshipResults[index].Disposition = "not_supported"
			reason := "not_supported_by_evidence"
			prepared.Response.RelationshipResults[index].Reason = &reason
			prepared.Response.RelationshipResults[index].Splits = nil
		}
	}
	commit, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, false)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipResults, 2)
	require.Equal(t, "not_stored", commit.RelationshipResults[0].Disposition)
	require.Equal(t, "not_supported_by_evidence", commit.RelationshipResults[0].Reason)
	require.Equal(t, "stored", commit.RelationshipResults[1].Disposition)

	prepared, scope = preparedCommitAssessment(t)
	for index := range prepared.Response.RelationshipResults {
		prepared.Response.RelationshipResults[index].Disposition = "not_supported"
		reason := "not_supported_by_evidence"
		prepared.Response.RelationshipResults[index].Reason = &reason
		prepared.Response.RelationshipResults[index].Splits = nil
	}
	commit, err = submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, false)
	require.NoError(t, err)
	for _, result := range commit.RelationshipResults {
		require.Equal(t, "not_stored", result.Disposition)
		require.Equal(t, "not_supported_by_evidence", result.Reason)
	}
}

func preparedCommitAssessment(t *testing.T) (*SynchronousAssessmentResult, repository.RememberCommitScope) {
	t.Helper()
	fixture := synchronousAssessmentFixture(t)
	prepared := validPreparedSynchronousAssessment(t, fixture)
	return prepared, repository.RememberCommitScope{
		TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID, IngestID: fixture.input.Scope.IngestID,
	}
}

func firstCommitRelationship(response *assessor.SemanticAssessmentResponse) *assessor.SemanticAssessmentRelationshipResult {
	if len(response.RelationshipResults) == 0 {
		panic("assessment response has no relationship results")
	}
	return &response.RelationshipResults[0]
}
