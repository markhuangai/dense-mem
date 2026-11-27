package topics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

type communityTestTransport struct {
	bodies []string
	calls  int
}

func (p *communityTestTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	response := p.bodies[p.calls]
	p.calls++
	return modelprovider.StructuredResult{Content: response}, nil
}

func TestCommunityPlacementAcceptsOnlySuppliedParent(t *testing.T) {
	parent := "1590f5eb-d4de-4794-8042-dfd4207b84f1"
	transport := &communityTestTransport{bodies: []string{
		`{"parent_id":"0feb8b44-b851-4535-9d7e-40d014743fee"}`,
		`{"parent_id":"` + parent + `"}`,
	}}
	service := CommunityAssessor{Transport: transport, Limits: assessor.SemanticAssessmentLimits{}}
	outcome, err := service.Assess(context.Background(), uuid.NewString(), contract.CommunityInput{
		Topic:      contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
		Candidates: []contract.CommunityCandidate{{ID: parent, Version: 1, Name: "Release readiness"}},
	}, "fixture")
	require.NoError(t, err)
	require.Equal(t, "placed", outcome.Status)
	require.Equal(t, parent, outcome.ParentID)
	require.Equal(t, 2, transport.calls)
}

func TestCommunityPlacementWithoutCandidatesIsStandalone(t *testing.T) {
	service := CommunityAssessor{}
	outcome, err := service.Assess(context.Background(), uuid.NewString(), contract.CommunityInput{
		Topic: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
	}, "fixture")
	require.NoError(t, err)
	require.Equal(t, "standalone", outcome.Status)
	require.NotEmpty(t, outcome.InputHash)
}

func TestCommunityBatchUsesOneValidatedDecision(t *testing.T) {
	parents := []string{uuid.NewString(), uuid.NewString()}
	inputs := make([]contract.CommunityInput, len(parents))
	for i := range inputs {
		inputs[i] = contract.CommunityInput{Topic: contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Specific"},
			Candidates: []contract.CommunityCandidate{{ID: parents[i], Version: 1, Name: "Broader"}}}
	}
	calls := 0
	complete := true
	service := CommunityAssessor{Settings: communityDecisionFixture{}, Decisions: testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		calls++
		require.Len(t, request.Groups, 2)
		confidence := .9
		answers := map[string]modelprovider.DecisionAnswer{
			"parent_0": {Type: modelprovider.DecisionChoice, Choice: parents[0], Confidence: &confidence, Probabilities: map[string]float64{"standalone": .2, parents[0]: .8}},
		}
		if complete {
			answers["parent_1"] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionChoice, Choice: parents[1], Confidence: &confidence, Probabilities: map[string]float64{"standalone": .201, parents[1]: .799}}
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})}
	outcomes, failures, err := service.AssessBatch(t.Context(), uuid.NewString(), inputs, "fixture")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.NoError(t, failures[0])
	require.NoError(t, failures[1])
	require.Equal(t, parents[0], outcomes[0].ParentID)
	require.Equal(t, "standalone", outcomes[1].Status)

	complete = false
	outcomes, failures, err = service.AssessBatch(t.Context(), uuid.NewString(), inputs, "fixture")
	require.NoError(t, err)
	require.Error(t, failures[0])
	require.Error(t, failures[1])
	require.Empty(t, outcomes[0].ParentID)
	require.Empty(t, outcomes[1].ParentID)
}

func TestCommunityPlacementRejectsWindowWithoutProviderInputCapacity(t *testing.T) {
	service := CommunityAssessor{contextWindow: 1}
	input := contract.CommunityInput{
		Topic:      contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
		Candidates: []contract.CommunityCandidate{{ID: uuid.NewString(), Version: 1, Name: "Release readiness"}},
	}
	outcome, err := service.Assess(t.Context(), uuid.NewString(), input, "fixture")
	require.ErrorIs(t, err, contract.ErrConfiguration)
	require.Empty(t, outcome.ParentID)
}

type communityDecisionFixture struct {
	answer modelprovider.DecisionAnswer
	err    error
}

func (f communityDecisionFixture) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return domain.DecisionRuntimeConfig{Revision: "fixture-policy", Effective: domain.DecisionTasks{CommunityPlacement: true}}, nil
}

func (f communityDecisionFixture) Decide(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{"parent": f.answer}}, f.err
}

func TestCommunityPlacementDecisionUsesAuthoritativeThreshold(t *testing.T) {
	parent := uuid.NewString()
	input := contract.CommunityInput{
		Topic:      contract.CommunityCandidate{ID: uuid.NewString(), Version: 1, Name: "Rollback drills"},
		Candidates: []contract.CommunityCandidate{{ID: parent, Version: 1, Name: "Release readiness"}},
	}
	unavailable := errors.New("decision provider unavailable")
	for _, tc := range []struct {
		name, choice, method, status, expectedParent string
		parentProbability                            float64
		err                                          error
	}{
		{name: "clear parent", choice: parent, parentProbability: .999, method: "decision", status: "placed", expectedParent: parent},
		{name: "clear standalone", choice: "standalone", parentProbability: .001, method: "decision", status: "standalone"},
		{name: "threshold parent", choice: parent, parentProbability: .8, method: "decision", status: "placed", expectedParent: parent},
		{name: "below threshold", choice: parent, parentProbability: .799, method: "decision", status: "standalone"},
		{name: "budget", err: modelprovider.ErrDecisionInputBudgetExceeded},
		{name: "outage", err: unavailable},
		{name: "invalid parent", choice: "foreign", parentProbability: .999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			teamID := uuid.NewString()
			ctx := observability.WithExchangeScope(t.Context(), observability.ExchangeScope{TeamID: teamID})
			confidence := 1.0
			provider := communityDecisionFixture{err: tc.err, answer: modelprovider.DecisionAnswer{
				Type: modelprovider.DecisionChoice, Choice: tc.choice, Confidence: &confidence,
				Probabilities: map[string]float64{parent: tc.parentProbability, "standalone": 1 - tc.parentProbability},
			}}
			generation := &communityTestTransport{bodies: []string{"{\"parent_id\":\"\"}"}}
			service := CommunityAssessor{Decisions: provider, Settings: provider, Transport: generation, Logger: logger, contextWindow: 1}
			outcome, err := service.Assess(ctx, uuid.NewString(), input, "fixture")
			if tc.method == "" {
				require.ErrorContains(t, err, "community placement decision")
				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				}
				if errors.Is(tc.err, modelprovider.ErrDecisionInputBudgetExceeded) {
					require.Contains(t, logs.String(), `"reason_code":"decision_input_budget_exceeded"`)
					require.Contains(t, logs.String(), `"generation_triggered":false`)
					require.Contains(t, logs.String(), `"team_id":"`+teamID+`"`)
				}
				require.Zero(t, generation.calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.method, outcome.Method)
			require.Equal(t, tc.status, outcome.Status)
			require.Equal(t, tc.expectedParent, outcome.ParentID)
			require.Equal(t, tc.method == "generation", generation.calls == 1)
			require.Contains(t, logs.String(), `"team_id":"`+teamID+`"`)
			require.Contains(t, logs.String(), `"policy_mode":"authoritative_closed_set"`)
			require.Contains(t, logs.String(), `"threshold":0.8`)
			require.Contains(t, logs.String(), `"choice":"`+tc.choice+`"`)
			require.Contains(t, logs.String(), fmt.Sprintf(`"winner_probability":%s`, strconv.FormatFloat(map[string]float64{parent: tc.parentProbability, "standalone": 1 - tc.parentProbability}[tc.choice], 'f', -1, 64)))
			require.Contains(t, logs.String(), `"generation_triggered":false`)
		})
	}
}
