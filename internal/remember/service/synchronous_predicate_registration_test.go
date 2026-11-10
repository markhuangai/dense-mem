package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestSynchronousAssessmentProviderFailureHasNoCompletedTurnTokens(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	fixture.deps.Metrics = metrics
	fixture.provider.err = errors.New("provider unavailable")

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Nil(t, prepared)
	require.ErrorIs(t, err, ErrRememberProviderUnavailable)
	require.Zero(t, SynchronousAssessmentProviderTurns(err))
	calls := metrics.AssessorCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "provider_error", calls[0].Outcome)
	require.Zero(t, calls[0].InputTokens)
	require.Zero(t, calls[0].OutputTokens)
}

func TestSynchronousAssessmentCountsServerRejectedProviderUsage(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_turn", true: "repair"}[repair], func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			metrics := observability.NewInMemoryDiscoverabilityMetrics()
			fixture.deps.Metrics = metrics
			failure := &assessor.MalformedResponseError{FailureClass: "input_budget", Attempts: 1, ValidationStage: "conversation_input_tokens"}
			usage := assessor.SemanticAssessmentTurn{InputTokens: 200001, OutputTokens: 9}
			wantInput, wantOutput := usage.InputTokens, usage.OutputTokens
			if repair {
				fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
					response := validSynchronousAssessmentResponse(request)
					response.RequestID = "wrong-request"
					response.InputTokens = 100
					response.OutputTokens = 10
					return response
				}
				fixture.provider.repairErr = failure
				fixture.provider.repairErrorTurn = usage
				wantInput += 100
				wantOutput += 10
			} else {
				fixture.provider.err = failure
				fixture.provider.errorTurn = usage
			}

			prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
			require.Nil(t, prepared)
			require.ErrorIs(t, err, ErrRememberInputBudgetExceeded)
			calls := metrics.AssessorCalls()
			require.Len(t, calls, 1)
			require.Equal(t, wantInput, calls[0].InputTokens)
			require.Equal(t, wantOutput, calls[0].OutputTokens)
		})
	}
}
