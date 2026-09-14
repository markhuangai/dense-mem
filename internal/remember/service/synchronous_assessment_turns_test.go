package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestSynchronousAssessmentClassifiesContextExpiryThroughProviderTimeout(t *testing.T) {
	for _, failedTurn := range []int{1, 2} {
		for _, test := range []struct {
			name         string
			want         error
			failureClass string
		}{
			{name: "phase deadline", want: ErrRememberRequestTimeout, failureClass: "timeout"},
			{name: "caller cancellation", want: context.Canceled, failureClass: "canceled"},
			{name: "provider timeout", want: ErrRememberProviderUnavailable, failureClass: "provider_unavailable"},
		} {
			phase := "initial"
			if failedTurn == 2 {
				phase = "repair"
			}
			t.Run(phase+"/"+test.name, func(t *testing.T) {
				fixture := synchronousAssessmentFixture(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if test.name == "phase deadline" {
					ctx = WithRememberDeadlines(ctx, time.Now().Add(time.Second-RememberAssessmentBudget))
				}
				fixture.provider.beforeCall = func(providerCtx context.Context) {
					if fixture.provider.calls+fixture.provider.repairCalls != failedTurn {
						return
					}
					switch test.name {
					case "phase deadline":
						<-providerCtx.Done()
					case "caller cancellation":
						cancel()
					}
				}
				timeout := &assessor.TimeoutError{Provider: "test", Message: "provider request timed out or was canceled"}
				if failedTurn == 1 {
					fixture.provider.err = timeout
				} else {
					fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
						response := validSynchronousAssessmentResponse(request)
						response.RequestID = "wrong-request"
						return response
					}
					fixture.provider.repairErr = timeout
				}

				result, err := AssessSynchronousRemember(ctx, fixture.deps, fixture.input)
				require.Nil(t, result)
				require.ErrorIs(t, err, test.want)
				require.Equal(t, failedTurn, fixture.provider.calls+fixture.provider.repairCalls)
				require.Equal(t, failedTurn-1, SynchronousAssessmentProviderTurns(err))
				if failedTurn == 2 {
					diagnostics := SynchronousAssessmentValidationDiagnostics(err)
					require.Equal(t, test.failureClass, diagnostics["failure_class"])
					turns := diagnostics["turns"].([]any)
					require.Len(t, turns, 1)
					require.Equal(t, []string{"request_id"}, turns[0].(map[string]any)["fields"])
				}
			})
		}
	}
}

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
