package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
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

func TestSubmissionAssessmentWorkerPersistsResultsForDeterministicPreflightFailure(t *testing.T) {
	_, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
	catalog.entityComplete = false

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	assert.True(t, processed)
	assert.Zero(t, provider.calls)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), completion.Status)
	assert.Equal(t, "entity_catalog", completion.Payload["failure_stage"])
	require.Len(t, completion.RelationshipResults, 3)
	for _, result := range completion.RelationshipResults {
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
		assert.Empty(t, result.Splits)
	}
}

func TestSubmissionAssessmentWorkerPersistsResultsWhenPreflightRetryExhausts(t *testing.T) {
	ledger, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.Attempts = ledger.run.MaxAttempts
	catalog.entityErr = errors.New("catalog unavailable")

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	assert.True(t, processed)
	assert.Zero(t, provider.calls)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), completion.Status)
	assert.Equal(t, "candidate_prefetch", completion.Payload["failure_stage"])
	require.Len(t, completion.RelationshipResults, 3)
	for _, result := range completion.RelationshipResults {
		assert.Equal(t, "not_stored", result.Disposition)
		assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
	}
}

func TestSubmissionAssessmentWorkerRequestsFallbackResultsWhenPlacementLoadRetryExhausts(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.Attempts = ledger.run.MaxAttempts
	ledger.getErr = errors.New("placement unavailable")

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Zero(t, provider.calls)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, "placement_load", completion.Payload["failure_stage"])
	assert.Empty(t, completion.RelationshipResults)
	assert.Equal(t, string(SubmissionErrorInternalFailure), completion.DefaultRelationshipResultReason)
}

func TestSubmissionAssessmentWorkerBoundsTurnsAcrossRevisionPersistenceFailure(t *testing.T) {
	tests := []struct {
		name         string
		revisionErrs []error
		wantTerminal bool
	}{
		{name: "retry persists before continuing", revisionErrs: []error{errors.New("temporary revision failure"), nil}},
		{name: "persistent failure terminalizes", revisionErrs: []error{errors.New("revision unavailable"), errors.New("revision unavailable")}, wantTerminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
			assessments.assessment = &repository.SubmissionAssessment{
				AssessmentID:       "00000000-0000-0000-0000-000000000903",
				ProviderTurns:      1,
				NormalizedResponse: json.RawMessage(`{}`),
				ResponseHash:       semanticAssessmentHash([]byte(`{}`)),
			}
			assessments.revisionErrors = append([]error(nil), test.revisionErrs...)

			processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

			require.NoError(t, err)
			assert.True(t, processed)
			assert.Equal(t, 1, provider.calls)
			assert.Len(t, assessments.revisionInputs, len(test.revisionErrs))
			assert.Empty(t, assessments.requeues)
			if !test.wantTerminal {
				assert.Empty(t, assessments.completions)
				require.Len(t, assessments.commits, 1)
				return
			}
			require.Len(t, assessments.completions, 1)
			completion := assessments.completions[0]
			assert.Equal(t, string(domain.SemanticReviewTerminalFailure), completion.Status)
			assert.Equal(t, "assessment_persist", completion.Payload["failure_stage"])
			require.Len(t, completion.RelationshipResults, 3)
			for _, result := range completion.RelationshipResults {
				assert.Equal(t, "not_stored", result.Disposition)
				assert.Equal(t, string(SubmissionErrorInternalFailure), result.Reason)
			}
		})
	}
}

func TestSubmissionAssessmentWorkerPersistsInvalidTurnsBeforeRepairFailureRetry(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	assessments.assessment = &repository.SubmissionAssessment{
		AssessmentID:       "00000000-0000-0000-0000-000000000904",
		ProviderTurns:      1,
		NormalizedResponse: json.RawMessage(`{}`),
		ResponseHash:       semanticAssessmentHash([]byte(`{}`)),
	}
	provider.responseForTurn = func(assessor.SemanticAssessmentRequest, int) (assessor.SemanticAssessmentResponse, error) {
		return assessor.SemanticAssessmentResponse{}, nil
	}
	provider.repairErr = &assessor.ProviderError{
		Provider: "stub", FailureClass: assessor.ProviderFailureClassHTTPServer, StatusCode: 503,
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, 2, provider.calls)
	require.Len(t, assessments.revisionInputs, 1)
	assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
	assert.Equal(t, 2, assessments.assessment.ProviderTurns)
	require.Len(t, assessments.requeues, 1)
	assert.Empty(t, assessments.completions)
}

func TestSubmissionAssessmentWorkerCarriesInitialSessionTurnsAcrossRetry(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.responseForTurn = func(assessor.SemanticAssessmentRequest, int) (assessor.SemanticAssessmentResponse, error) {
		return assessor.SemanticAssessmentResponse{}, nil
	}
	provider.repairErr = &assessor.ProviderError{
		Provider: "stub", FailureClass: assessor.ProviderFailureClassHTTPServer, StatusCode: 503,
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 2, provider.calls)
	require.Len(t, assessments.requeues, 1)
	assert.Equal(t, 1, assessments.requeues[0].AssessorTurnsReserved)
	assert.Nil(t, assessments.assessment)

	ledger.run.Attempts++
	ledger.run.AssessorTurnsReserved = assessments.requeues[0].AssessorTurnsReserved
	assessments.loadCalls = 0
	assessments.reserved = false
	assessments.requeues = nil
	provider.calls = 0
	provider.responseForTurn = nil
	provider.repairErr = nil

	processed, err = worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, provider.calls)
	require.NotNil(t, assessments.assessment)
	assert.Equal(t, 2, assessments.assessment.ProviderTurns)
	require.Len(t, assessments.commits, 1)
}

func TestSubmissionAssessmentWorkerStopsBeforeProviderWhenInitialTurnBudgetIsReserved(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.AssessorTurnsReserved = SemanticPlacementMaxAssessorTurns

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Zero(t, provider.calls)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, SemanticPlacementMaxAssessorTurns, assessments.completions[0].Payload["assessor_turns"])
	assert.Equal(t, "malformed_exhausted", assessments.completions[0].Payload["failure_class"])
}

func TestSubmissionAssessmentWorkerPersistsCommitRepairTurnsBeforeProviderFailureRetry(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.responseForTurn = func(req assessor.SemanticAssessmentRequest, turns int) (assessor.SemanticAssessmentResponse, error) {
		if turns == 1 {
			return submissionAssessmentValidResponse(req, false), nil
		}
		return assessor.SemanticAssessmentResponse{}, nil
	}
	provider.repairErrors = []error{nil, &assessor.ProviderError{
		Provider: "stub", FailureClass: assessor.ProviderFailureClassHTTPServer, StatusCode: 503,
	}}
	assessments.commitErrors = []error{repository.ErrSubmissionPredicateRegistrationHeld}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 3, provider.calls)
	require.Len(t, assessments.revisionInputs, 1)
	assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
	require.NotNil(t, assessments.assessment)
	assert.Equal(t, 2, assessments.assessment.ProviderTurns)
	require.Len(t, assessments.requeues, 1)
	assert.Zero(t, assessments.requeues[0].AssessorTurnsReserved)
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

func TestSubmissionAssessmentWorkerTerminalizesDatabaseFailureWithoutRelationshipResults(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.Attempts = ledger.run.MaxAttempts
	assessments.commitErr = &pgconn.PgError{Code: "XX000"}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), completion.Status)
	assert.Equal(t, "semantic_commit", completion.Payload["failure_stage"])
	assert.Equal(t, "database_failure", completion.Payload["failure_class"])
	assert.Equal(t, string(SubmissionErrorDatabaseFailure), completion.Payload["failure_code"])
	assert.Empty(t, completion.RelationshipResults)
	assert.Empty(t, completion.DefaultRelationshipResultReason)
}

func TestSubmissionAssessmentWorkerRetryOrFailDatabaseFailureWithoutRelationshipResults(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	terminalRun := *ledger.run
	terminalRun.Attempts = terminalRun.MaxAttempts
	scope := submissionAssessmentRunScope(terminalRun, service.workerID)

	err := service.retryOrFail(
		context.Background(), terminalRun, scope, "placement_load", false, false,
		&pgconn.PgError{Code: "08006"},
	)

	require.NoError(t, err)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, "placement_load", completion.Payload["failure_stage"])
	assert.Equal(t, "database_failure", completion.Payload["failure_class"])
	assert.Equal(t, string(SubmissionErrorDatabaseFailure), completion.Payload["failure_code"])
	assert.Empty(t, completion.RelationshipResults)
	assert.Empty(t, completion.DefaultRelationshipResultReason)
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
	canonical, err := assessor.CanonicalJSON(raw)
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
	canonical, err := assessor.CanonicalJSON(raw)
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
	provider.responseForTurn = func(req assessor.SemanticAssessmentRequest, turns int) (assessor.SemanticAssessmentResponse, error) {
		if turns == 1 {
			return submissionAssessmentValidResponse(req, false), nil
		}
		return assessor.SemanticAssessmentResponse{}, nil
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

func TestSubmissionAssessmentWorkerDoesNotPersistResultsWhenProviderRetriesExhausted(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	ledger.run.Attempts = ledger.run.MaxAttempts
	provider.startErr = &assessor.ProviderError{
		Provider: "stub", FailureClass: assessor.ProviderFailureClassHTTPServer, StatusCode: 503,
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.completions, 1)
	completion := assessments.completions[0]
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), completion.Status)
	assert.Equal(t, "assessment", completion.Payload["failure_stage"])
	assert.Equal(t, "http_5xx", completion.Payload["failure_class"])
	assert.Equal(t, string(SubmissionErrorProviderUnavailable), completion.Payload["failure_code"])
	assert.Empty(t, completion.RelationshipResults)
	assert.Empty(t, completion.DefaultRelationshipResultReason)
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
		assessor.FailureMeasurement{Unit: "tokens", Observed: 101, Limit: 100},
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
