package topics

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
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

func TestCommunityPlacementDecisionFallsBackOnlyOnUncertaintyOrBudget(t *testing.T) {
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
		{name: "uncertain", choice: parent, parentProbability: .7, method: "generation", status: "standalone"},
		{name: "budget", err: modelprovider.ErrDecisionInputBudgetExceeded, method: "generation", status: "standalone"},
		{name: "outage", err: unavailable},
		{name: "invalid parent", choice: "foreign", parentProbability: .999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			confidence := 1.0
			provider := communityDecisionFixture{err: tc.err, answer: modelprovider.DecisionAnswer{
				Type: modelprovider.DecisionChoice, Choice: tc.choice, Confidence: &confidence,
				Probabilities: map[string]float64{parent: tc.parentProbability, "standalone": 1 - tc.parentProbability},
			}}
			generation := &communityTestTransport{bodies: []string{"{\"parent_id\":\"\"}"}}
			service := CommunityAssessor{Decisions: provider, Settings: provider, Transport: generation}
			outcome, err := service.Assess(t.Context(), uuid.NewString(), input, "fixture")
			if tc.method == "" {
				require.ErrorContains(t, err, "community placement decision")
				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				}
				require.Zero(t, generation.calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.method, outcome.Method)
			require.Equal(t, tc.status, outcome.Status)
			require.Equal(t, tc.expectedParent, outcome.ParentID)
			require.Equal(t, tc.method == "generation", generation.calls == 1)
		})
	}
}
