package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitCommitRetainsEvidenceAndSourceIdentity(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	prepared := validPreparedSynchronousAssessment(t, fixture)
	input := SynchronousRememberCommitRequest{TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID, IngestID: fixture.input.Scope.IngestID, SpaceID: fixture.input.Scope.SpaceID, SpaceGeneration: 1, IdempotencyKey: "operation", RequestHash: "sha256:exact", Assessment: prepared}
	for _, fragment := range fixture.input.Snapshot.Evidence {
		input.Evidence = append(input.Evidence, repository.EvidenceInput{FragmentID: fragment.FragmentID, Content: fragment.Content, SourceKey: fragment.SourceID, SourceRevisionToken: fragment.SourceRevisionID})
	}
	commit, err := BuildSynchronousRememberCommitInput(input)
	require.NoError(t, err)
	require.Equal(t, input.Evidence, commit.Evidence)
	require.Equal(t, input.TeamID, commit.TeamID)
	require.Equal(t, input.OwnerProfileID, commit.OwnerProfileID)
	require.Equal(t, input.SpaceID, commit.SpaceID)
	require.Equal(t, input.RequestHash, commit.RequestHash)
	require.Len(t, commit.Commit.Items, 2)
	require.Len(t, commit.EvidenceSecurityResults, 2)
	require.Len(t, commit.DuplicateResolutions, 2)
	for i, resolution := range commit.DuplicateResolutions {
		require.Equal(t, "new", resolution.Disposition)
		require.Equal(t, input.Evidence[i].FragmentID, resolution.InputFragmentID)
	}
}

func TestEvidenceOnlyUnitCommitRejectsUnavailableDuplicateSelection(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	candidateID := uuid.NewString()
	fixture.input.Snapshot.Items[0].DuplicateAssessmentRequired = true
	fixture.input.Snapshot.DuplicateCandidates = []repository.RememberDuplicateCandidateGroup{{EvidenceIndex: 0, Candidates: []repository.RememberDuplicateCandidate{{FragmentID: candidateID, OwnerProfileID: uuid.NewString(), Content: "Alpha uses Beta."}}}}
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		response.EvidenceEquivalenceResults[0].Action = "reuse"
		response.EvidenceEquivalenceResults[0].CandidateEvidenceID = &candidateID
		return response
	}
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	resolutions, err := submissionAssessmentDuplicateResolutions(prepared.Plan, prepared.Response)
	require.NoError(t, err)
	require.Equal(t, candidateID, resolutions[0].CandidateFragmentID)
	require.Equal(t, fixture.input.Snapshot.DuplicateCandidates[0].Candidates[0].OwnerProfileID, resolutions[0].CandidateOwnerID)
	prepared.Response.EvidenceEquivalenceResults = nil
	_, err = submissionAssessmentDuplicateResolutions(prepared.Plan, prepared.Response)
	require.ErrorContains(t, err, "duplicate result is missing")
	foreign := uuid.NewString()
	prepared.Response.EvidenceEquivalenceResults = []assessor.SemanticAssessmentEvidenceEquivalenceResult{{EvidenceID: "evidence:0", Action: "reuse", CandidateEvidenceID: &foreign}}
	_, err = submissionAssessmentDuplicateResolutions(prepared.Plan, prepared.Response)
	require.ErrorContains(t, err, "unknown candidate")
}

func TestEvidenceOnlyUnitCommitFencesOnlyReferencedKnownEvidence(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	prepared := validPreparedSynchronousAssessment(t, fixture)
	knownID, unusedID := uuid.NewString(), uuid.NewString()
	known := repository.SubmissionAssessmentKnownEvidence{TeamID: fixture.input.Scope.TeamID, EvidenceID: knownID, FragmentID: knownID, OwnerProfileID: uuid.NewString(), Content: "Alpha does not use Beta.", ContentHash: "canonical-hash", SpaceID: fixture.input.Scope.SpaceID, SpaceGeneration: 1}
	prepared.Plan.knownEvidenceByID[knownID] = known
	prepared.Plan.knownEvidenceByID[unusedID] = repository.SubmissionAssessmentKnownEvidence{EvidenceID: unusedID}
	prepared.Response.EvidenceConflictResults = []assessor.SemanticAssessmentEvidenceConflictResult{{Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{{EvidenceID: "evidence:0", Start: 0, End: 5}, {EvidenceID: knownID, Start: 0, End: 5}}}}
	scope := repository.RememberCommitScope{TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID, IngestID: fixture.input.Scope.IngestID}
	commit, err := submissionAssessmentCommitInput(scope, prepared.Plan, prepared.Response, &prepared.Assessment, false)
	require.NoError(t, err)
	require.Equal(t, []repository.SubmissionAssessmentKnownEvidence{known}, commit.KnownEvidenceSnapshot)
	require.Len(t, commit.EvidenceConflictResults, 1)
	require.Equal(t, knownID, commit.EvidenceConflictResults[0].Positions[1].EvidenceID)
}
