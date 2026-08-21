package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSubmissionAssessmentWorkerClassifiesCommitOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		commitErr   error
		commitNil   bool
		wantStatus  string
		wantStage   string
		wantRequeue bool
		wantPayload bool
		wantError   bool
		wantIssue   string
	}{
		{
			name:       "non-promotable assessment requires review",
			commitErr:  repository.ErrSubmissionAssessmentNonPromotable,
			wantStatus: string(domain.SemanticReviewReviewRequired),
			wantStage:  "commit_review",
			wantIssue:  "semantic_commit_non_promotable",
		},
		{
			name:       "predicate registration hold requires review",
			commitErr:  repository.ErrSubmissionPredicateRegistrationHeld,
			wantStatus: string(domain.SemanticReviewReviewRequired),
			wantStage:  "commit_review",
			wantIssue:  "predicate_registration_conflict",
		},
		{
			name:       "stale conflict context requires review",
			commitErr:  repository.ErrConflictContextStale,
			wantStatus: string(domain.SemanticReviewReviewRequired),
			wantStage:  "conflict_context_stale",
		},
		{
			name:       "scope mismatch terminalizes",
			commitErr:  repository.ErrSubmissionAssessmentScopeMismatch,
			wantStatus: string(domain.SemanticReviewTerminalFailure),
			wantStage:  "assessment_scope",
		},
		{
			name:        "transient commit failure requeues",
			commitErr:   errors.New("commit unavailable"),
			wantStage:   "semantic_commit",
			wantRequeue: true,
			wantPayload: true,
		},
		{
			name:        "nil commit result requeues",
			commitNil:   true,
			wantStage:   "semantic_commit",
			wantRequeue: true,
			wantPayload: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
			assessments.commitErr = test.commitErr
			assessments.commitNil = test.commitNil

			processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

			assert.True(t, processed)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if test.wantRequeue {
				require.Len(t, assessments.requeues, 1)
				assert.Equal(t, "submission_assessment_attempt", assessments.requeues[0].OutcomeKind)
				if test.wantPayload {
					assert.Equal(t, test.wantStage, assessments.requeues[0].Payload["failure_stage"])
					assert.NotEmpty(t, assessments.requeues[0].Payload["failure_reason_code"])
				} else {
					assert.Nil(t, assessments.requeues[0].Payload)
				}
				return
			}
			require.Len(t, assessments.completions, 1)
			assert.Equal(t, test.wantStatus, assessments.completions[0].Status)
			if test.wantStatus == string(domain.SemanticReviewReviewRequired) {
				assert.Equal(t, test.wantStage, assessments.completions[0].Payload["review_stage"])
				assert.NotContains(t, assessments.completions[0].Payload, "failure_reason_code")
				assert.NotContains(t, assessments.completions[0].Payload, "failure_class")
			} else {
				assert.Equal(t, test.wantStage, assessments.completions[0].Payload["failure_stage"])
			}
			if test.wantIssue != "" {
				issues, ok := assessments.completions[0].Payload["hold_issues"].([]map[string]any)
				require.True(t, ok)
				require.Len(t, issues, 1)
				assert.Equal(t, test.wantIssue, issues[0]["code"])
			}
		})
	}
}

func TestSubmissionAssessmentWorkerRequeuesRepositoryFailuresByStage(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*submissionAssessmentWorkerAssessmentStub)
		stage   string
		payload bool
	}{
		{
			name: "assessment load",
			mutate: func(assessments *submissionAssessmentWorkerAssessmentStub) {
				assessments.loadErr = errors.New("assessment read failed")
			},
			stage:   "assessment",
			payload: true,
		},
		{
			name: "assessor reservation",
			mutate: func(assessments *submissionAssessmentWorkerAssessmentStub) {
				assessments.reserveErr = errors.New("reservation failed")
			},
			stage:   "assessment",
			payload: true,
		},
		{
			name: "assessment persistence",
			mutate: func(assessments *submissionAssessmentWorkerAssessmentStub) {
				assessments.persistErr = errors.New("assessment persistence failed")
			},
			stage:   "assessment",
			payload: true,
		},
		{
			name: "confidence policy",
			mutate: func(assessments *submissionAssessmentWorkerAssessmentStub) {
				assessments.policyErr = errors.New("policy unavailable")
			},
			stage:   "confidence_policy",
			payload: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
			test.mutate(assessments)

			processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

			require.NoError(t, err)
			assert.True(t, processed)
			require.Len(t, assessments.requeues, 1)
			assert.Equal(t, "submission_assessment_attempt", assessments.requeues[0].OutcomeKind)
			if test.payload {
				assert.Equal(t, test.stage, assessments.requeues[0].Payload["failure_stage"])
				assert.NotEmpty(t, assessments.requeues[0].Payload["failure_reason_code"])
			} else {
				assert.Nil(t, assessments.requeues[0].Payload)
			}
		})
	}
}

func TestSubmissionAssessmentWorkerRejectsNilCompletionAndRetryResults(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")

	assessments.completeNil = true
	err := service.completeTerminal(context.Background(), scope, string(domain.SemanticReviewTerminalFailure), "failed", "test")
	require.ErrorContains(t, err, "placement worker persistence failed")
	failure, ok := placementWorkerFailureFromError(err)
	require.True(t, ok)
	require.Equal(t, "unknown", failure.Stage)

	assessments.completeNil = false
	assessments.requeueNil = true
	err = service.retryOrFail(context.Background(), *ledger.run, scope, "test", false, false)
	require.ErrorContains(t, err, "placement worker persistence failed")
	failure, ok = placementWorkerFailureFromError(err)
	require.True(t, ok)
	require.Equal(t, "unknown", failure.Stage)
}

func TestSubmissionAssessmentWorkerPreservesFailureCauseWhenAttemptsAreExhausted(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")
	terminalRun := *ledger.run
	terminalRun.MaxAttempts = terminalRun.Attempts
	cause := deterministicSemanticAssessmentPreflightErrorWithMeasurement(
		"assessment_input",
		"input exceeds bound",
		verifier.FailureMeasurement{Unit: "tokens", Observed: 101, Limit: 100},
	)

	require.NoError(t, service.retryOrFail(context.Background(), terminalRun, scope, "assessment_input", false, false, cause))
	require.Len(t, assessments.completions, 1)
	payload := assessments.completions[0].Payload
	require.Equal(t, "validation_failed", payload["failure_class"])
	require.Equal(t, "assessment_input_token_limit_exceeded", payload["failure_reason_code"])
	require.Equal(t, map[string]any{"unit": "tokens", "observed": 101, "limit": 100}, payload["failure_measurement"])
}

func TestSubmissionAssessmentWorkerPreservesOriginalErrorWhenTerminalCompletionFails(t *testing.T) {
	t.Run("assessment attempt consumed", func(t *testing.T) {
		_, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
		assessments.reserved = true
		completionErr := errors.New("terminal completion unavailable")
		assessments.completeErr = completionErr

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

		require.True(t, processed)
		require.Error(t, err)
		assert.ErrorIs(t, err, repository.ErrSubmissionAssessorAttemptConsumed)
		assert.ErrorIs(t, err, completionErr)
	})

	t.Run("replacement conflict", func(t *testing.T) {
		_, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
		assessments.commitErr = repository.ErrSubmissionReplacementConflict
		completionErr := errors.New("replacement terminal completion unavailable")
		assessments.completeErr = completionErr

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

		require.True(t, processed)
		require.Error(t, err)
		assert.ErrorIs(t, err, repository.ErrSubmissionReplacementConflict)
		assert.ErrorIs(t, err, completionErr)
	})
}
