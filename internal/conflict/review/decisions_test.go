package conflictreview

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/conflict/assessment"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

type conflictDecisionFunction func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error)

func (f conflictDecisionFunction) Decide(ctx context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return f(ctx, request)
}

type conflictDecisionSettings struct{ snapshot domain.DecisionRuntimeConfig }

func (s conflictDecisionSettings) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return s.snapshot, nil
}

func TestConflictDecisionGatesRemainDistinctFromVendorConfidence(t *testing.T) {
	for _, test := range []struct {
		name                          string
		position, support, confidence float64
		selected                      bool
	}{
		{"probability_boundary", 0.99, 0.99, 0.01, true},
		{"high_vendor_confidence_is_insufficient", 0.98, 0.99, 1, false},
		{"support_gate", 0.99, 0.98, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newConflictReviewRepositoryStub(t)
			generative := &conflictReviewProviderStub{response: conflictassessment.ConflictAssessmentResponse{Decision: "abstain", Rationale: "The dossier does not establish an answer."}}
			service := newConflictReviewService(t, repo, generative)
			service.decisionSettings = conflictDecisionSettings{snapshot: domain.DecisionRuntimeConfig{Revision: "revision", Provider: domain.DecisionProviderStatus{Configured: true, Provider: "openai", Model: "gpt-6-luna"}, Effective: domain.DecisionTasks{ConflictReview: true}}}
			service.decisions = conflictDecisionFunction(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				require.Equal(t, "revision", request.SettingsVersion)
				require.Len(t, request.Groups[0].Questions, 2)
				return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
					"position":               {Type: modelprovider.DecisionChoice, Choice: conflictReviewTestPositionAID, Confidence: &test.confidence, Probabilities: map[string]float64{conflictReviewTestPositionAID: test.position, conflictReviewTestPositionBID: 1 - test.position}},
					"sufficiently_supported": {Type: modelprovider.DecisionPredicate, Probability: &test.support},
				}}, nil
			})
			_, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
			require.NoError(t, err)
			require.Len(t, repo.reserveInputs, 1)
			require.True(t, repo.reserveInputs[0].RequireProvider)
			require.Len(t, repo.completions, 1)
			if test.selected {
				require.Empty(t, generative.requests)
				require.Equal(t, "selected", repo.completions[0].Decision)
				require.Equal(t, 0.99, *repo.completions[0].Confidence, "legacy persisted field is explicitly identified as position probability in event metadata")
				require.Equal(t, "select_typed_position", repo.completions[0].DecisionDiagnostics.Action)
				require.Equal(t, 0.01, *repo.completions[0].DecisionDiagnostics.Result.Answers["position"].Confidence)
			} else {
				require.Len(t, generative.requests, 1)
				require.Equal(t, "abstained", repo.completions[0].Decision)
				require.Equal(t, "uncertain_position_or_support", repo.completions[0].DecisionDiagnostics.Reason)
			}
		})
	}
}

func TestConflictDecisionProviderFailureNeverResolvesByFallback(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.completeResult.FailureCount = 100
	generation := &conflictReviewProviderStub{}
	service := newConflictReviewService(t, repo, generation)
	service.decisionSettings = conflictDecisionSettings{snapshot: domain.DecisionRuntimeConfig{Provider: domain.DecisionProviderStatus{Configured: true, Model: "gpt-6-luna"}, Effective: domain.DecisionTasks{ConflictReview: true}}}
	failure := &modelprovider.ProviderError{Provider: "openai", FailureClass: "http_4xx", StatusCode: 401}
	service.decisions = conflictDecisionFunction(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		return modelprovider.DecisionResult{}, failure
	})
	_, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.True(t, errors.Is(err, failure))
	require.Empty(t, repo.applyInputs)
	require.Empty(t, generation.requests)
	require.Len(t, repo.reserveInputs, 1)
	require.True(t, repo.reserveInputs[0].RequireProvider)
	require.Len(t, repo.completions, 1)
	require.Equal(t, "failed", repo.completions[0].Decision)
}
