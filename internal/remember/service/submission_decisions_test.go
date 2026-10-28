package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

type decisionFunction func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error)

func (f decisionFunction) Decide(ctx context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return f(ctx, request)
}

func decisionRoutingFixture() (assessor.SemanticAssessmentRequest, *domain.DecisionRuntimeConfig) {
	return assessor.SemanticAssessmentRequest{RequestID: "request", Evidence: []assessor.SemanticReviewEvidence{{EvidenceID: "submitted", Content: "The active team limit is 4; the next-window limit is 8."}}, EvidenceEquivalenceCandidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "submitted", Candidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "a", Content: "The active team limit is 4; the next-window limit is 8.", BoundaryText: "do-not-send-expanded-boundaries"}, {EvidenceID: "b", Content: "The active team limit is 8; the next-window limit is 4."}}}}}, &domain.DecisionRuntimeConfig{Revision: "frozen-settings", Provider: domain.DecisionProviderStatus{Configured: true, Provider: "openai", Model: "gpt-6-luna"}, Effective: domain.DecisionTasks{SecurityReview: true, Deduplication: true}}
}

func TestTypedSubmissionPolicyProbabilityGates(t *testing.T) {
	for _, test := range []struct {
		name                  string
		first, second, hazard float64
		action                string
		security              bool
	}{
		{"reuse_boundary", 0.99, 0.01, 0.01, "reuse", true},
		{"new_boundary", 0.01, 0.01, 0.01, "new", true},
		{"uncertain", 0.98, 0.01, 0.01, "generate", true},
		{"alternative_uncertain", 0.99, 0.02, 0.01, "generate", true},
		{"multiple_matches", 0.99, 0.99, 0.01, "generate", true},
		{"threat_uncertain", 0.99, 0.01, 0.02, "reuse", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, snapshot := decisionRoutingFixture()
			provider := decisionFunction(func(ctx context.Context, req modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				require.Equal(t, "frozen-settings", req.SettingsVersion)
				encoded, err := modelprovider.DecisionInputHash(req.Groups)
				require.NoError(t, err)
				require.NotEmpty(t, encoded)
				require.NotContains(t, fmt.Sprint(req.Groups), "do-not-send-expanded-boundaries")
				require.Contains(t, fmt.Sprint(req.Groups), "next-window limit is 8")
				answers := map[string]modelprovider.DecisionAnswer{}
				for _, group := range req.Groups {
					for _, question := range group.Questions {
						probability := test.hazard
						if question.Name == "dedup_0_0" {
							probability = test.first
						}
						if question.Name == "dedup_0_1" {
							probability = test.second
						}
						answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
					}
				}
				return modelprovider.DecisionResult{Answers: answers}, nil
			})
			routing, err := routeSubmissionDecisions(context.Background(), provider, snapshot, request, nil)
			require.NoError(t, err)
			if test.action == "generate" {
				require.Nil(t, routing.Equivalence)
			} else {
				require.Len(t, routing.Equivalence, 1)
				require.Equal(t, test.action, routing.Equivalence[0].Action)
				if test.action == "reuse" {
					require.Equal(t, "a", *routing.Equivalence[0].CandidateEvidenceID)
				}
			}
			if test.security {
				require.Len(t, routing.Security, 1)
				require.Equal(t, "pass", routing.Security[0].Decision)
				require.Empty(t, routing.Security[0].Signals)
			} else {
				require.Nil(t, routing.Security, "uncertainty retains exact-span generation for the complete security task")
			}
			require.Len(t, routing.Stages, 2)
			require.NotEmpty(t, routing.Stages[0].InputHash)
		})
	}
}

func TestTypedSubmissionInvalidOrUnavailableResultFailsCompletely(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		answer modelprovider.DecisionAnswer
	}{
		{name: "authentication", err: &modelprovider.ProviderError{FailureClass: "http_4xx", StatusCode: 401}},
		{name: "cancellation", err: context.Canceled},
		{name: "missing_answers"},
		{name: "unknown_answers", answer: modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, snapshot := decisionRoutingFixture()
			provider := decisionFunction(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				answers := map[string]modelprovider.DecisionAnswer{}
				if strings.HasPrefix(test.name, "unknown") {
					answers["not_authorized"] = test.answer
				}
				return modelprovider.DecisionResult{Answers: answers}, test.err
			})
			routing, err := routeSubmissionDecisions(context.Background(), provider, snapshot, request, nil)
			require.Error(t, err)
			require.Nil(t, routing)
			if test.err != nil {
				require.True(t, errors.Is(err, test.err))
			}
		})
	}
}

func TestSubmissionSettingsSnapshotAndTaskOverrides(t *testing.T) {
	request, snapshot := decisionRoutingFixture()
	snapshot.Effective.Deduplication = false
	snapshot.Overrides.Deduplication = true
	provider := decisionFunction(func(_ context.Context, req modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		answers := map[string]modelprovider.DecisionAnswer{}
		for _, group := range req.Groups {
			for _, question := range group.Questions {
				require.NotContains(t, question.Name, "dedup")
				probability := 0.001
				answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
			}
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	routing, err := routeSubmissionDecisions(context.Background(), provider, snapshot, request, nil)
	require.NoError(t, err)
	require.Nil(t, routing.Equivalence)
	require.NotNil(t, routing.Security)
	require.True(t, routing.Stages[0].Settings.Overrides.Deduplication)
	snapshot.Revision = "next-operation"
	snapshot.Effective.SecurityReview = false
	require.Equal(t, "frozen-settings", routing.Stages[0].Settings.Revision)
	require.True(t, routing.Stages[0].Settings.Effective.SecurityReview)
}

func TestDecisionAdversarialPolicyCohortPreservesCompleteMeaningAndRoutesUncertainty(t *testing.T) {
	var cases []struct {
		Name, Submitted, Candidate, Action string
		Equivalence, Hazard                float64
		Safe                               bool
	}
	body, err := os.ReadFile("testdata/decision_adversarial.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &cases))
	require.GreaterOrEqual(t, len(cases), 9)
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			request, snapshot := decisionRoutingFixture()
			request.Evidence[0].Content = test.Submitted
			request.EvidenceEquivalenceCandidates[0].Candidates = request.EvidenceEquivalenceCandidates[0].Candidates[:1]
			request.EvidenceEquivalenceCandidates[0].Candidates[0].Content = test.Candidate
			provider := decisionFunction(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				encoded, err := json.Marshal(request.Groups[0].Input)
				require.NoError(t, err)
				require.Contains(t, string(encoded), test.Submitted)
				require.Contains(t, string(encoded), test.Candidate)
				answers := map[string]modelprovider.DecisionAnswer{}
				for _, group := range request.Groups {
					for _, q := range group.Questions {
						p := test.Hazard
						if strings.HasPrefix(q.Name, "dedup_") {
							p = test.Equivalence
							require.Contains(t, q.Instructions, "temporal qualifiers")
							require.Contains(t, q.Instructions, "polarity")
						}
						answers[q.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &p}
					}
				}
				return modelprovider.DecisionResult{Answers: answers}, nil
			})
			routing, err := routeSubmissionDecisions(context.Background(), provider, snapshot, request, nil)
			require.NoError(t, err)
			require.Equal(t, test.Safe, routing.Security != nil)
			if test.Action == "generate" {
				require.Nil(t, routing.Equivalence)
			} else {
				require.Len(t, routing.Equivalence, 1)
				require.Equal(t, test.Action, routing.Equivalence[0].Action)
			}
		})
	}
}
