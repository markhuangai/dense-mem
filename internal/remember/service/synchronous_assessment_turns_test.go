package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestSynchronousAssessmentBoundsRepeatedProviderTurnNumbers(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.repairTurn = 1
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		response.RequestID = "wrong-request"
		return response
	}

	_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
	require.Equal(t, SemanticMaxAssessorTurns-1, fixture.provider.repairCalls)
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(err))
	diagnostics := SynchronousAssessmentValidationDiagnostics(err)
	turns := diagnostics["turns"].([]any)
	require.Len(t, turns, SemanticMaxAssessorTurns)
}

func TestSynchronousAssessmentIgnoresInflatedProviderTurnNumbers(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.assessTurn = SemanticMaxAssessorTurns
	fixture.provider.repairTurn = SemanticMaxAssessorTurns
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		response.RequestID = "wrong-request"
		return response
	}

	_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
	require.Equal(t, SemanticMaxAssessorTurns-1, fixture.provider.repairCalls)
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(err))

	fixture = synchronousAssessmentFixture(t)
	fixture.provider.assessTurn = SemanticMaxAssessorTurns + 10
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return validSynchronousAssessmentResponse(request)
	}
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	require.Equal(t, 1, prepared.Assessment.ProviderTurns)
}
