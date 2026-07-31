package conflictreview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	conflictReviewTestTeamID      = "00000000-0000-0000-0000-000000000101"
	conflictReviewTestConflictID  = "00000000-0000-0000-0000-000000000102"
	conflictReviewTestReviewRunID = "00000000-0000-0000-0000-000000000103"
	conflictReviewTestAttemptID   = "00000000-0000-0000-0000-000000000104"
	conflictReviewTestPositionAID = "00000000-0000-0000-0000-000000000201"
	conflictReviewTestPositionBID = "00000000-0000-0000-0000-000000000202"
	conflictReviewTestFragmentAID = "00000000-0000-0000-0000-000000000301"
	conflictReviewTestFragmentBID = "00000000-0000-0000-0000-000000000302"
	conflictReviewTestSupportAID  = "00000000-0000-0000-0000-000000000401"
	conflictReviewTestSupportBID  = "00000000-0000-0000-0000-000000000402"
	conflictReviewTestTaskID      = "00000000-0000-0000-0000-000000000501"
	conflictReviewTestWorkerID    = "conflict-review-test"
)

func TestServiceResolvesSelectedAssessment(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:      verifier.ConflictAssessmentDecisionSelect,
			PositionID:    pointer(conflictReviewTestPositionBID),
			Confidence:    0.91,
			Rationale:     "The supplied evidence supports this position.",
			ProviderTurns: 2,
		},
	}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "overdue_ai", result.Stage)
	assert.Equal(t, "ai", result.ResolutionMethod)
	assert.Equal(t, conflictReviewTestPositionBID, result.PreferredPositionID)
	assert.Equal(t, conflictReviewTestAttemptID, result.AssessmentAttemptID)
	require.Len(t, repo.completions, 1)
	assert.Equal(t, "selected", repo.completions[0].Decision)
	assert.NotEmpty(t, repo.completions[0].ResponseHash)
	require.Len(t, repo.applyInputs, 1)
	assert.Equal(t, "ai", repo.applyInputs[0].Method)
	assert.Equal(t, conflictReviewTestPositionBID, repo.applyInputs[0].PreferredPositionID)
	require.Len(t, provider.requests, 1)
	assert.Equal(t, conflictReviewTestConflictID, provider.requests[0].CaseID)
	assert.Len(t, provider.requests[0].Evidence, 2)
	assert.Equal(t, 2, provider.requests[0].Positions[0].OwnerProfileCount)
}

func TestServiceUsesLastWriteWinsAfterExplicitAbstention(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:      verifier.ConflictAssessmentDecisionAbstain,
			Confidence:    0,
			Rationale:     "The dossier is not clear enough to choose.",
			ProviderTurns: 1,
		},
	}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "overdue_last_write_wins", result.Stage)
	assert.Equal(t, "last_write_wins", result.ResolutionMethod)
	assert.Equal(t, conflictReviewTestPositionAID, result.PreferredPositionID)
	require.Len(t, repo.completions, 1)
	assert.Equal(t, "abstained", repo.completions[0].Decision)
	require.Len(t, repo.applyInputs, 1)
	assert.Equal(t, "last_write_wins", repo.applyInputs[0].Method)
	assert.Equal(t, conflictReviewTestPositionAID, repo.applyInputs[0].PreferredPositionID)
}

func TestServiceUsesLastWriteWinsAfterFifthFailedAssessment(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.completeResult.FailureCount = 5
	provider := &conflictReviewProviderStub{err: &verifier.ProviderError{
		Provider:     "test",
		Message:      "provider failed",
		FailureClass: verifier.ProviderFailureClassHTTPServer,
	}}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "last_write_wins", result.ResolutionMethod)
	require.Len(t, repo.completions, 1)
	assert.Equal(t, "failed", repo.completions[0].Decision)
	assert.Equal(t, verifier.ProviderFailureClassHTTPServer, repo.completions[0].FailureClass)
	require.Len(t, repo.applyInputs, 1)
	assert.Equal(t, "last_write_wins", repo.applyInputs[0].Method)
}

func TestServiceLeavesFailedAssessmentPendingWhenCompletionResultIsNil(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.completeNil = true
	provider := &conflictReviewProviderStub{err: &verifier.ProviderError{
		Provider:     "test",
		Message:      "provider failed",
		FailureClass: verifier.ProviderFailureClassHTTPServer,
	}}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, "overdue_assessment_failed", result.Stage)
	assert.Empty(t, repo.applyInputs)
}

func TestServiceUsesLastWriteWinsAfterAbandonedAssessmentRecoveryWithoutProviderCall(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.reservation.LastWriteWins = true
	provider := &conflictReviewProviderStub{err: errors.New("provider must not be called")}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
	assert.Equal(t, "overdue_last_write_wins", result.Stage)
	assert.Equal(t, "last_write_wins", result.ResolutionMethod)
	assert.Empty(t, provider.requests)
	assert.Empty(t, repo.completions)
	require.Len(t, repo.applyInputs, 1)
	assert.Equal(t, "last_write_wins", repo.applyInputs[0].Method)
}

func TestServiceResumesPendingResolutionWithoutProviderCall(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.pendingFound = true
	repo.pendingResult = &repository.ApplyOverdueConflictResolutionResult{
		ConflictID:          conflictReviewTestConflictID,
		PreferredPositionID: conflictReviewTestPositionAID,
		Method:              "ai",
		Pending:             true,
	}
	provider := &conflictReviewProviderStub{err: errors.New("provider must not be called")}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeOverdue, result.Outcome)
	assert.Equal(t, "resolution_pending", result.Stage)
	assert.True(t, result.ResolutionPending)
	assert.Equal(t, conflictReviewTestPositionAID, result.PreferredPositionID)
	assert.Equal(t, 0, len(provider.requests))
	assert.Empty(t, repo.reserveInputs)
}

func TestServiceRecordsLowConfidenceSelectionAsFailedAssessment(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:   verifier.ConflictAssessmentDecisionSelect,
			PositionID: pointer(conflictReviewTestPositionAID),
			Confidence: AssessmentConfidenceThreshold - 0.01,
			Rationale:  "The evidence is incomplete.",
		},
	}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, "overdue_assessment_failed", result.Stage)
	require.Len(t, repo.completions, 1)
	assert.Equal(t, "failed", repo.completions[0].Decision)
	assert.Equal(t, "below_confidence_threshold", repo.completions[0].FailureClass)
	assert.Empty(t, repo.applyInputs)
}

func TestServiceTreatsStaleAssessmentCompletionAsNoOp(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.completeErr = repository.ErrConflictAssessmentReserved
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:   verifier.ConflictAssessmentDecisionSelect,
			PositionID: pointer(conflictReviewTestPositionAID),
			Confidence: 0.91,
			Rationale:  "The evidence supports this position.",
		},
	}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, "overdue_assessment_stale", result.Stage)
	assert.Empty(t, repo.applyInputs)
}

func TestServiceLeavesUnresolvableLastWriteWinsCaseOverdue(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	for index := range repo.dossier.Positions {
		repo.dossier.Positions[index].Supports = nil
	}
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:   verifier.ConflictAssessmentDecisionAbstain,
			Rationale:  "The supplied evidence is inconclusive.",
			Confidence: 0,
		},
	}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeOverdue, result.Outcome)
	assert.Equal(t, "overdue_last_write_wins_unavailable", result.Stage)
	assert.Empty(t, repo.applyInputs)
}

func TestServiceRecordsDerivedEvidenceStagingFailure(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.applyResult.DerivedEvidence = []repository.ConflictDerivedEvidenceTarget{{
		TaskID:               conflictReviewTestTaskID,
		TeamID:               conflictReviewTestTeamID,
		ConflictID:           conflictReviewTestConflictID,
		SystemProfileID:      conflictReviewTestTeamID,
		TargetFragmentID:     conflictReviewTestFragmentAID,
		TargetOwnerProfileID: conflictReviewTestTeamID,
		SelectedPositionID:   conflictReviewTestPositionBID,
		SourceGroupKey:       "source-a",
	}}
	repo.stageErr = errors.New("derived evidence staging failed")
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:   verifier.ConflictAssessmentDecisionSelect,
			PositionID: pointer(conflictReviewTestPositionBID),
			Confidence: 0.91,
			Rationale:  "The evidence supports this position.",
		},
	}
	service := newConflictReviewService(t, repo, provider)

	_, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.ErrorIs(t, err, repo.stageErr)
	require.Len(t, repo.stagedTargets, 1)
	require.Len(t, repo.recordedFailures, 1)
	assert.Equal(t, "staging_failed", repo.recordedFailures[0].failureClass)
}

func TestServiceProcessesPendingDerivedEvidence(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.derivedBatches = [][]repository.ConflictDerivedEvidenceTarget{{{
		TaskID:               conflictReviewTestTaskID,
		TeamID:               conflictReviewTestTeamID,
		ConflictID:           conflictReviewTestConflictID,
		SystemProfileID:      conflictReviewTestTeamID,
		TargetFragmentID:     conflictReviewTestFragmentAID,
		TargetOwnerProfileID: conflictReviewTestTeamID,
		SelectedPositionID:   conflictReviewTestPositionAID,
		SourceGroupKey:       "source-a",
	}}}
	service := newConflictReviewService(t, repo, &conflictReviewProviderStub{})

	staged, err := service.ProcessPendingConflictDerivedEvidence(context.Background(), repository.ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      conflictReviewTestTeamID,
		ReviewRunID: conflictReviewTestReviewRunID,
		WorkerID:    conflictReviewTestWorkerID,
		Limit:       2,
		Lease:       time.Minute,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, staged)
	require.Len(t, repo.derivedClaimInputs, 1)
	require.Len(t, repo.stagedTargets, 1)
}

func TestServiceReportsPendingDerivedEvidenceFailures(t *testing.T) {
	t.Run("service is not configured", func(t *testing.T) {
		var service *Service
		_, err := service.ProcessPendingConflictDerivedEvidence(context.Background(), repository.ClaimConflictDerivedEvidenceTasksInput{})
		require.EqualError(t, err, "conflict review service is not configured")
	})

	t.Run("claim failure", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.derivedClaimErr = errors.New("derived evidence claim failed")
		service := newConflictReviewService(t, repo, &conflictReviewProviderStub{})

		staged, err := service.ProcessPendingConflictDerivedEvidence(context.Background(), repository.ClaimConflictDerivedEvidenceTasksInput{
			TeamID:      conflictReviewTestTeamID,
			ReviewRunID: conflictReviewTestReviewRunID,
			WorkerID:    conflictReviewTestWorkerID,
			Limit:       1,
			Lease:       time.Minute,
		})

		require.ErrorIs(t, err, repo.derivedClaimErr)
		assert.Zero(t, staged)
	})
}

func TestServiceLeavesAlreadyReservedAssessmentUntouched(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.reserved = false
	provider := &conflictReviewProviderStub{err: errors.New("provider must not be called")}
	service := newConflictReviewService(t, repo, provider)

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, "overdue_assessment_already_reserved", result.Stage)
	assert.Empty(t, provider.requests)
	assert.Empty(t, repo.completions)
}

func TestNewRejectsInvalidConflictReviewDependencies(t *testing.T) {
	provider := &conflictReviewProviderStub{}
	_, err := New(Dependencies{Provider: provider})
	require.EqualError(t, err, "conflict review service: repository is required")

	repo := newConflictReviewRepositoryStub(t)
	_, err = New(Dependencies{Repository: repo})
	require.EqualError(t, err, "conflict review service: provider is required")

	_, err = New(Dependencies{Repository: repo, Provider: emptyModelConflictReviewProvider{}})
	require.EqualError(t, err, "conflict review service: provider model is required")

	_, err = New(Dependencies{Repository: repo, Provider: provider, Timezone: "not/a-timezone"})
	require.ErrorContains(t, err, "timezone is invalid")
}

func TestNewRunnerRejectsInvalidDependencies(t *testing.T) {
	provider := &conflictReviewProviderStub{}
	_, err := NewRunner(nil, provider, "UTC", verifier.DefaultSemanticAssessmentLimits())
	require.EqualError(t, err, "conflict review runner: ledger is required")

	ledger := newConflictReviewRunLedgerStub(t)
	_, err = NewRunner(ledger, emptyModelConflictReviewProvider{}, "UTC", verifier.DefaultSemanticAssessmentLimits())
	require.EqualError(t, err, "conflict review service: provider model is required")
}

func TestRunnerExecutesConflictReviewLifecycle(t *testing.T) {
	ledger := newConflictReviewRunLedgerStub(t)
	provider := &conflictReviewProviderStub{
		response: verifier.ConflictAssessmentResponse{
			Decision:   verifier.ConflictAssessmentDecisionSelect,
			PositionID: pointer(conflictReviewTestPositionBID),
			Confidence: 0.91,
			Rationale:  "The supplied evidence supports this position.",
		},
	}
	runner, err := NewRunner(ledger, provider, "UTC", verifier.DefaultSemanticAssessmentLimits())
	require.NoError(t, err)

	run, claimed, err := runner.ReserveRelationshipConflictReviewRun(context.Background(), repository.ConflictReviewRunInput{
		TeamID:       conflictReviewTestTeamID,
		WorkerID:     conflictReviewTestWorkerID,
		LocalRunDate: time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC),
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, conflictReviewTestReviewRunID, run.ReviewRunID)

	cases, err := runner.ClaimRelationshipConflictCases(context.Background(), repository.ClaimRelationshipConflictCasesInput{
		TeamID:      conflictReviewTestTeamID,
		ReviewRunID: conflictReviewTestReviewRunID,
		WorkerID:    conflictReviewTestWorkerID,
		Limit:       1,
		Lease:       time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, cases)

	reviewed, err := runner.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, "overdue_ai", reviewed.Stage)
	assert.Equal(t, conflictReviewTestPositionBID, reviewed.PreferredPositionID)

	staged, err := runner.ProcessPendingConflictDerivedEvidence(context.Background(), repository.ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      conflictReviewTestTeamID,
		ReviewRunID: conflictReviewTestReviewRunID,
		WorkerID:    conflictReviewTestWorkerID,
		Limit:       1,
		Lease:       time.Minute,
	})
	require.NoError(t, err)
	assert.Zero(t, staged)

	err = runner.CompleteRelationshipConflictReviewRun(context.Background(), repository.ConflictReviewRunCompleteInput{
		TeamID:      conflictReviewTestTeamID,
		ReviewRunID: conflictReviewTestReviewRunID,
		WorkerID:    conflictReviewTestWorkerID,
		Status:      "completed",
	})
	require.NoError(t, err)
	assert.Len(t, ledger.reserveRunInputs, 1)
	assert.Len(t, ledger.claimInputs, 1)
	assert.Len(t, ledger.completeRunInputs, 1)
	assert.Len(t, ledger.derivedClaimInputs, 1)
}

func TestServiceStopsBeforeOverdueAssessmentWhenReviewCannotProceed(t *testing.T) {
	t.Run("service is not configured", func(t *testing.T) {
		var service *Service
		_, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
		require.EqualError(t, err, "conflict review service is not configured")
	})

	t.Run("deterministic review error", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.reviewErr = errors.New("review failed")
		_, err := newConflictReviewService(t, repo, &conflictReviewProviderStub{}).ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
		require.ErrorIs(t, err, repo.reviewErr)
	})

	t.Run("deterministic review result is required", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.reviewResult = nil
		_, err := newConflictReviewService(t, repo, &conflictReviewProviderStub{}).ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
		require.EqualError(t, err, "conflict review service: deterministic review returned no result")
	})

	t.Run("already resolved deterministic case does not reserve an assessment", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.reviewResult.Outcome = repository.ConflictReviewOutcomeResolve
		input := conflictReviewInput()
		input.Now = time.Time{}
		result, err := newConflictReviewService(t, repo, &conflictReviewProviderStub{}).ReviewRelationshipConflictCase(context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
		assert.Empty(t, repo.reserveInputs)
	})

	t.Run("pending resolution lookup error", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.resumeErr = errors.New("resume failed")
		_, err := newConflictReviewService(t, repo, &conflictReviewProviderStub{}).ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
		require.ErrorIs(t, err, repo.resumeErr)
	})

	t.Run("assessment reservation error", func(t *testing.T) {
		repo := newConflictReviewRepositoryStub(t)
		repo.reserveErr = errors.New("reserve failed")
		_, err := newConflictReviewService(t, repo, &conflictReviewProviderStub{}).ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
		require.ErrorIs(t, err, repo.reserveErr)
	})
}

func newConflictReviewService(t *testing.T, repo *conflictReviewRepositoryStub, provider *conflictReviewProviderStub) *Service {
	t.Helper()
	service, err := New(Dependencies{
		Repository: repo,
		Provider:   provider,
		Timezone:   "UTC",
		Limits:     verifier.DefaultSemanticAssessmentLimits(),
		Now: func() time.Time {
			return time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
		},
	})
	require.NoError(t, err)
	return service
}

func conflictReviewInput() repository.ReviewRelationshipConflictCaseInput {
	return repository.ReviewRelationshipConflictCaseInput{
		TeamID:      conflictReviewTestTeamID,
		ConflictID:  conflictReviewTestConflictID,
		ReviewRunID: conflictReviewTestReviewRunID,
		WorkerID:    conflictReviewTestWorkerID,
		Now:         time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
}

func newConflictReviewRepositoryStub(t *testing.T) *conflictReviewRepositoryStub {
	t.Helper()
	older := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	dossier := &repository.OverdueConflictAssessmentDossier{
		TeamID:      conflictReviewTestTeamID,
		ConflictID:  conflictReviewTestConflictID,
		CaseVersion: 3,
		Question:    "Which state is current?",
		Positions: []repository.OverdueConflictAssessmentPosition{
			{
				PositionID:        conflictReviewTestPositionAID,
				PositionKey:       "value:a",
				OwnerProfileCount: 2,
				Supports:          []domain.ConflictResolutionSupport{{Authority: "primary", AcceptedAt: older}},
			},
			{
				PositionID:  conflictReviewTestPositionBID,
				PositionKey: "value:b",
				Supports:    []domain.ConflictResolutionSupport{{Authority: "secondary", AcceptedAt: newer}},
			},
		},
		Evidence: []repository.OverdueConflictAssessmentEvidence{
			{
				FragmentID:     conflictReviewTestFragmentAID,
				OwnerProfileID: conflictReviewTestTeamID,
				PositionID:     conflictReviewTestPositionAID,
				SupportID:      conflictReviewTestSupportAID,
				SourceGroupKey: "source-a",
				Authority:      "primary",
				AcceptedAt:     older,
				Content:        "Evidence for position A.",
			},
			{
				FragmentID:     conflictReviewTestFragmentBID,
				OwnerProfileID: conflictReviewTestTeamID,
				PositionID:     conflictReviewTestPositionBID,
				SupportID:      conflictReviewTestSupportBID,
				SourceGroupKey: "source-b",
				Authority:      "secondary",
				AcceptedAt:     newer,
				Content:        "Evidence for position B.",
			},
		},
	}
	return &conflictReviewRepositoryStub{
		reviewResult: &repository.ReviewRelationshipConflictCaseResult{
			ConflictID: conflictReviewTestConflictID,
			Outcome:    repository.ConflictReviewOutcomeOverdue,
			Stage:      repository.ConflictReviewStageDueNoWinner,
		},
		reservation: &repository.OverdueConflictAssessmentReservation{
			AssessmentAttemptID: conflictReviewTestAttemptID,
			CaseVersion:         3,
			Model:               "test-model",
			PolicyVersion:       domain.ConflictOverduePolicyVersion,
		},
		dossier: dossier,
		applyResult: &repository.ApplyOverdueConflictResolutionResult{
			ConflictID:          conflictReviewTestConflictID,
			PreferredPositionID: conflictReviewTestPositionBID,
			Resolved:            true,
		},
		reserved: true,
	}
}

type conflictReviewRepositoryStub struct {
	reviewResult    *repository.ReviewRelationshipConflictCaseResult
	reservation     *repository.OverdueConflictAssessmentReservation
	dossier         *repository.OverdueConflictAssessmentDossier
	completeResult  repository.CompleteOverdueConflictAssessmentResult
	completeNil     bool
	applyResult     *repository.ApplyOverdueConflictResolutionResult
	pendingFound    bool
	pendingResult   *repository.ApplyOverdueConflictResolutionResult
	reserved        bool
	reviewErr       error
	resumeErr       error
	reserveErr      error
	completeErr     error
	applyErr        error
	derivedClaimErr error
	stageErr        error
	recordStageErr  error

	reserveInputs      []repository.ReserveOverdueConflictAssessmentInput
	completions        []repository.CompleteOverdueConflictAssessmentInput
	applyInputs        []repository.ApplyOverdueConflictResolutionInput
	derivedClaimInputs []repository.ClaimConflictDerivedEvidenceTasksInput
	derivedBatches     [][]repository.ConflictDerivedEvidenceTarget
	stagedTargets      []repository.ConflictDerivedEvidenceTarget
	recordedFailures   []conflictReviewRecordedFailure
}

type conflictReviewRunLedgerStub struct {
	*conflictReviewRepositoryStub
	run                repository.ConflictReviewRunRecord
	claimed            bool
	reserveRunInputs   []repository.ConflictReviewRunInput
	claimInputs        []repository.ClaimRelationshipConflictCasesInput
	completeRunInputs  []repository.ConflictReviewRunCompleteInput
	claimedCaseRecords []repository.RelationshipConflictCaseRecord
}

func newConflictReviewRunLedgerStub(t *testing.T) *conflictReviewRunLedgerStub {
	t.Helper()
	return &conflictReviewRunLedgerStub{
		conflictReviewRepositoryStub: newConflictReviewRepositoryStub(t),
		run: repository.ConflictReviewRunRecord{
			TeamID:      conflictReviewTestTeamID,
			ReviewRunID: conflictReviewTestReviewRunID,
			Status:      "running",
			WorkerID:    conflictReviewTestWorkerID,
		},
		claimed: true,
	}
}

func (s *conflictReviewRunLedgerStub) ReserveRelationshipConflictReviewRun(_ context.Context, input repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error) {
	s.reserveRunInputs = append(s.reserveRunInputs, input)
	run := s.run
	return &run, s.claimed, nil
}

func (s *conflictReviewRunLedgerStub) ClaimRelationshipConflictCases(_ context.Context, input repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error) {
	s.claimInputs = append(s.claimInputs, input)
	return append([]repository.RelationshipConflictCaseRecord(nil), s.claimedCaseRecords...), nil
}

func (s *conflictReviewRunLedgerStub) CompleteRelationshipConflictReviewRun(_ context.Context, input repository.ConflictReviewRunCompleteInput) error {
	s.completeRunInputs = append(s.completeRunInputs, input)
	return nil
}

type conflictReviewRecordedFailure struct {
	target       repository.ConflictDerivedEvidenceTarget
	failureClass string
}

func (s *conflictReviewRepositoryStub) ReviewRelationshipConflictCase(_ context.Context, _ repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error) {
	if s.reviewErr != nil {
		return nil, s.reviewErr
	}
	if s.reviewResult == nil {
		return nil, nil
	}
	copy := *s.reviewResult
	return &copy, nil
}

func (s *conflictReviewRepositoryStub) ResumePendingOverdueConflictResolution(_ context.Context, _ repository.ResumePendingOverdueConflictResolutionInput) (*repository.ApplyOverdueConflictResolutionResult, bool, error) {
	if s.resumeErr != nil {
		return nil, false, s.resumeErr
	}
	if !s.pendingFound {
		return nil, false, nil
	}
	copy := *s.pendingResult
	return &copy, true, nil
}

func (s *conflictReviewRepositoryStub) ReserveOverdueConflictAssessment(_ context.Context, input repository.ReserveOverdueConflictAssessmentInput) (*repository.OverdueConflictAssessmentReservation, *repository.OverdueConflictAssessmentDossier, bool, error) {
	s.reserveInputs = append(s.reserveInputs, input)
	if s.reserveErr != nil {
		return nil, nil, false, s.reserveErr
	}
	if !s.reserved {
		return nil, nil, false, nil
	}
	reservation := *s.reservation
	return &reservation, s.dossier, true, nil
}

func (s *conflictReviewRepositoryStub) CompleteOverdueConflictAssessment(_ context.Context, input repository.CompleteOverdueConflictAssessmentInput) (*repository.CompleteOverdueConflictAssessmentResult, error) {
	s.completions = append(s.completions, input)
	if s.completeErr != nil {
		return nil, s.completeErr
	}
	if s.completeNil {
		return nil, nil
	}
	copy := s.completeResult
	return &copy, nil
}

func (s *conflictReviewRepositoryStub) ApplyOverdueConflictResolution(_ context.Context, input repository.ApplyOverdueConflictResolutionInput) (*repository.ApplyOverdueConflictResolutionResult, error) {
	s.applyInputs = append(s.applyInputs, input)
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	copy := *s.applyResult
	copy.PreferredPositionID = input.PreferredPositionID
	copy.Method = input.Method
	return &copy, nil
}

func (s *conflictReviewRepositoryStub) ClaimConflictDerivedEvidenceTasks(_ context.Context, input repository.ClaimConflictDerivedEvidenceTasksInput) ([]repository.ConflictDerivedEvidenceTarget, error) {
	s.derivedClaimInputs = append(s.derivedClaimInputs, input)
	if s.derivedClaimErr != nil {
		return nil, s.derivedClaimErr
	}
	if len(s.derivedBatches) == 0 {
		return nil, nil
	}
	batch := s.derivedBatches[0]
	s.derivedBatches = s.derivedBatches[1:]
	return batch, nil
}

func (s *conflictReviewRepositoryStub) StageConflictDerivedEvidence(_ context.Context, target repository.ConflictDerivedEvidenceTarget) (*repository.StageConflictDerivedEvidenceResult, error) {
	s.stagedTargets = append(s.stagedTargets, target)
	if s.stageErr != nil {
		return nil, s.stageErr
	}
	return &repository.StageConflictDerivedEvidenceResult{}, nil
}

func (s *conflictReviewRepositoryStub) RecordConflictDerivedEvidenceFailure(_ context.Context, target repository.ConflictDerivedEvidenceTarget, failureClass string) error {
	s.recordedFailures = append(s.recordedFailures, conflictReviewRecordedFailure{target: target, failureClass: failureClass})
	if s.recordStageErr != nil {
		return s.recordStageErr
	}
	return nil
}

type conflictReviewProviderStub struct {
	response verifier.ConflictAssessmentResponse
	err      error
	requests []verifier.ConflictAssessmentRequest
}

func (s *conflictReviewProviderStub) AssessRelationshipConflict(_ context.Context, request verifier.ConflictAssessmentRequest) (verifier.ConflictAssessmentResponse, error) {
	s.requests = append(s.requests, request)
	return s.response, s.err
}

func (s *conflictReviewProviderStub) ModelName() string {
	return "test-model"
}

type emptyModelConflictReviewProvider struct{}

func (emptyModelConflictReviewProvider) AssessRelationshipConflict(context.Context, verifier.ConflictAssessmentRequest) (verifier.ConflictAssessmentResponse, error) {
	return verifier.ConflictAssessmentResponse{}, nil
}

func (emptyModelConflictReviewProvider) ModelName() string {
	return ""
}

func pointer(value string) *string {
	return &value
}
