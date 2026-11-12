package service

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitAssessmentUsesExactEvidenceInIndexOrder(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	snapshot := cloneRememberAssessmentSnapshot(fixture.input.Snapshot)
	snapshot.Evidence[0].Content = "  café 🧭\n"
	snapshot.Items[0].Fragment = snapshot.Evidence[0]
	snapshot.Items[0], snapshot.Items[1] = snapshot.Items[1], snapshot.Items[0]
	fixture.input.Snapshot = snapshot
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	require.Equal(t, "  café 🧭\n", prepared.Request.Evidence[0].Content)
	require.Equal(t, "evidence:0", prepared.Request.Evidence[0].EvidenceID)
	require.Equal(t, snapshot.Evidence[0].SourceRevisionID, prepared.Request.Evidence[0].SourceRevisionID)
	require.Equal(t, fixture.input.Scope.TeamID, prepared.Request.TeamID)
	require.Empty(t, prepared.Request.KnownEvidence)
	require.Equal(t, snapshot.Evidence[0].FragmentID, prepared.Plan.Items[0].Fragment.FragmentID)
}

func TestEvidenceOnlyUnitAssessmentRejectsIncompleteStagedEvidence(t *testing.T) {
	for _, mutate := range []func(*RememberAssessmentSnapshot){
		func(s *RememberAssessmentSnapshot) { s.Items = s.Items[:1] },
		func(s *RememberAssessmentSnapshot) { s.Items[1] = s.Items[0] },
		func(s *RememberAssessmentSnapshot) { s.Evidence[1].EvidenceIndex = 0 },
		func(s *RememberAssessmentSnapshot) { s.Items[0].Fragment.FragmentID = "different" },
		func(s *RememberAssessmentSnapshot) { s.MaxEvidenceItems = 1 },
	} {
		fixture := synchronousAssessmentFixture(t)
		mutate(&fixture.input.Snapshot)
		_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
		require.Error(t, err)
		require.Zero(t, fixture.provider.calls)
	}
	fixture := synchronousAssessmentFixture(t)
	_, err := AssessSynchronousRemember(context.Background(), SynchronousAssessmentDependencies{}, fixture.input)
	require.ErrorContains(t, err, "provider is required")
	fixture.input.Scope.OwnerProfileID = ""
	_, err = AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.ErrorContains(t, err, "authenticated scope is required")
}

func TestEvidenceOnlyUnitAssessmentRegeneratesTheWholeInvalidResponse(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, turn int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		if turn == 1 {
			response.EvidenceSecurityResults = response.EvidenceSecurityResults[:1]
		}
		return response
	}
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	require.Equal(t, 1, fixture.provider.calls)
	require.Equal(t, 1, fixture.provider.repairCalls)
	require.Len(t, prepared.Response.EvidenceSecurityResults, 2)
	require.Equal(t, 2, prepared.Assessment.ProviderTurns)
	require.NotEmpty(t, fixture.provider.repairErrors)
}
