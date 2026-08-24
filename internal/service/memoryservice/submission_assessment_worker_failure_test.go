package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
			name:       "stale conflict context is stale input",
			commitErr:  repository.ErrConflictContextStale,
			wantStatus: string(domain.SemanticReviewRejected),
			wantStage:  "exact_reference_preflight",
		},
		{
			name:       "stale exact reference is stale input",
			commitErr:  repository.ErrRememberExactReferenceStale,
			wantStatus: string(domain.SemanticReviewRejected),
			wantStage:  "exact_reference_preflight",
		},
		{
			name:       "stale correction target is stale input",
			commitErr:  repository.ErrCorrectionTargetStale,
			wantStatus: string(domain.SemanticReviewRejected),
			wantStage:  "exact_reference_preflight",
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
			assert.Equal(t, test.wantStage, assessments.completions[0].Payload["failure_stage"])
			if test.wantStatus == string(domain.SemanticReviewRejected) {
				assert.Equal(t, string(SubmissionErrorStaleInput), assessments.completions[0].Payload["failure_code"])
			}
		})
	}
}

func TestSubmissionAssessmentWorkerPersistsDatabaseFailureCode(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")

	err := service.completeTerminal(
		context.Background(), scope,
		string(domain.SemanticReviewTerminalFailure), "failed", "semantic_commit",
		&pgconn.PgError{Code: "08006"},
	)

	require.NoError(t, err)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, string(SubmissionErrorDatabaseFailure), assessments.completions[0].Payload["failure_code"])
}

func TestSubmissionAssessmentWorkerRepairsCommitRacesInSameSession(t *testing.T) {
	for _, race := range []error{
		repository.ErrSubmissionAssessmentNonPromotable,
		repository.ErrSubmissionPredicateRegistrationHeld,
	} {
		t.Run(race.Error(), func(t *testing.T) {
			_, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
			assessments.commitErrors = []error{race, nil}

			processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

			require.NoError(t, err)
			require.True(t, processed)
			require.Len(t, assessments.commits, 2)
			require.Len(t, assessments.revisionInputs, 1)
			assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
			assert.Equal(t, 2, provider.calls)
			assert.Equal(t, provider.startSessionID, provider.repairSessionID)
			assert.Len(t, catalog.entityInputs, 2)
			assert.Empty(t, assessments.requeues)
			assert.Empty(t, assessments.completions)
		})
	}
}

func TestSubmissionAssessmentWorkerRecoversPersistedCommitRaceWithFreshSession(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	response := submissionAssessmentValidResponse(request, false)
	response.ProviderTurns = 1
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	canonical, err := verifier.CanonicalJSON(raw)
	require.NoError(t, err)
	assessments.assessment = &repository.SubmissionAssessment{
		TeamID: ledger.run.TeamID, AssessmentID: "00000000-0000-0000-0000-000000000901",
		OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID, PlacementRunID: ledger.run.PlacementRunID,
		RequestID: request.RequestID, AssessorContractVersion: domain.ContractVersion, Model: provider.ModelName(), Tokenizer: assessmentTokenizer(service.limits),
		ProviderTurns: 1, InputTokens: request.InputTokens, OutputTokens: response.OutputTokens,
		CandidateContextTokens: request.CandidateContextTokens, NormalizedResponse: canonical, ResponseHash: semanticAssessmentHash(canonical),
	}
	assessments.commitErrors = []error{repository.ErrSubmissionPredicateRegistrationHeld, nil}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.commits, 2)
	require.Len(t, assessments.revisionInputs, 1)
	assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
	assert.Equal(t, 1, provider.calls)
	assert.Empty(t, assessments.requeues)
}

func TestSubmissionAssessmentWorkerStopsPersistedCommitRepairAtSubmissionTurnBound(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	response := submissionAssessmentValidResponse(request, false)
	response.ProviderTurns = SemanticPlacementMaxAssessorTurns
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	canonical, err := verifier.CanonicalJSON(raw)
	require.NoError(t, err)
	assessments.assessment = &repository.SubmissionAssessment{
		TeamID: ledger.run.TeamID, AssessmentID: "00000000-0000-0000-0000-000000000902",
		OwnerProfileID: ledger.run.OwnerProfileID, IngestID: ledger.run.IngestID, PlacementRunID: ledger.run.PlacementRunID,
		RequestID: request.RequestID, AssessorContractVersion: domain.ContractVersion, Model: provider.ModelName(), Tokenizer: assessmentTokenizer(service.limits),
		ProviderTurns: SemanticPlacementMaxAssessorTurns, InputTokens: request.InputTokens, OutputTokens: response.OutputTokens,
		CandidateContextTokens: request.CandidateContextTokens, NormalizedResponse: canonical, ResponseHash: semanticAssessmentHash(canonical),
	}
	assessments.commitErrors = []error{repository.ErrSubmissionPredicateRegistrationHeld}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Zero(t, provider.calls)
	assert.Empty(t, assessments.revisionInputs)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(SubmissionErrorInternalFailure), completion.Payload["failure_code"])
	assert.Equal(t, "commit_race_exhausted", completion.Payload["failure_stage"])
	require.Len(t, completion.RelationshipResults, 3)
	for _, result := range completion.RelationshipResults {
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
		assert.Empty(t, result.Splits)
	}
}

func TestSubmissionAssessmentWorkerPreservesResultsWhenCommitRepairTurnsMalformed(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.responseForTurn = func(req verifier.SemanticAssessmentRequest, turns int) (verifier.SemanticAssessmentResponse, error) {
		if turns == 1 {
			return submissionAssessmentValidResponse(req, false), nil
		}
		return verifier.SemanticAssessmentResponse{}, nil
	}
	assessments.commitErrors = []error{repository.ErrSubmissionPredicateRegistrationHeld}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, SemanticPlacementMaxAssessorTurns, provider.calls)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(SubmissionErrorProviderResponseInvalid), completion.Payload["failure_code"])
	assert.Equal(t, "assessment", completion.Payload["failure_stage"])
	assert.Equal(t, "malformed_exhausted", completion.Payload["failure_class"])
	require.Len(t, completion.RelationshipResults, 3)
	for _, result := range completion.RelationshipResults {
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
		assert.Empty(t, result.Splits)
	}
}

func TestSubmissionAssessmentWorkerPersistsResultsWhenProviderRetriesExhausted(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.Attempts = ledger.run.MaxAttempts
	provider.startErr = &verifier.ProviderError{
		Provider: "stub", FailureClass: verifier.ProviderFailureClassHTTPServer, StatusCode: 503,
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	require.Len(t, completion.RelationshipResults, 3)
	for _, result := range completion.RelationshipResults {
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
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

}
