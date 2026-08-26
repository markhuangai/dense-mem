package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type boundedAssessmentSession struct{}

func (boundedAssessmentSession) SessionID() string { return "bounded-session" }

type boundedAssessmentProvider struct {
	repairs int
}

func (p *boundedAssessmentProvider) Assess(context.Context, assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	return boundedAssessmentSession{}, assessor.SemanticAssessmentTurn{
		Turn:             1,
		ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "invalid"}},
	}, nil
}

func (p *boundedAssessmentProvider) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	p.repairs++
	return assessor.SemanticAssessmentTurn{
		Turn:             p.repairs + 1,
		ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "invalid"}},
	}, nil
}

func (*boundedAssessmentProvider) ModelName() string { return "test-assessor" }

func TestCompleteRememberSessionStopsAfterThreeProviderTurns(t *testing.T) {
	provider := &boundedAssessmentProvider{}
	engine := &assessmentEngine{
		provider: provider,
		limits:   assessor.DefaultSemanticAssessmentLimits(),
		metrics:  observability.NoopDiscoverabilityMetrics(),
	}
	request := assessor.SemanticAssessmentRequest{RequestID: "bounded-request"}
	_, _, err := engine.completeRememberSessionTurns(
		context.Background(),
		boundedAssessmentSession{},
		providerTurn(1),
		request,
		func(context.Context) (assessor.SemanticAssessmentRequest, error) { return request, nil },
		0,
	)
	var malformed *assessor.MalformedResponseError
	require.True(t, errors.As(err, &malformed))
	require.Equal(t, 3, malformed.Attempts)
	require.Equal(t, 2, provider.repairs)
}

func providerTurn(turn int) assessor.SemanticAssessmentTurn {
	return assessor.SemanticAssessmentTurn{
		Turn:             turn,
		ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "invalid"}},
	}
}
