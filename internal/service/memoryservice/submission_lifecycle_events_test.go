package memoryservice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestRememberLogsAcceptedSubmissionWithoutEvidence(t *testing.T) {
	logger, logs := submissionLifecycleTestLogger()
	teamID, profileID, keyID := uuid.New(), uuid.New(), uuid.New()
	ledger := &rememberLedgerStub{result: &repository.CreateIngestResult{
		TeamID: teamID.String(), OwnerProfileID: profileID.String(), IngestID: uuid.NewString(),
		Status: string(domain.PlacementRunQueued), Attempts: 0, MaxAttempts: 5,
	}}
	svc := NewRememberService(RememberDependencies{Ledger: ledger, Logger: logger})

	_, err := svc.Remember(authenticatedRememberContext(teamID, profileID, keyID), RememberRequest{
		IdempotencyKey:    "lifecycle-accepted",
		Evidence:          []RememberEvidenceInput{{Content: "private evidence must not be logged"}},
		RelationshipHints: completeRememberRelationshipHints(1),
	})
	require.NoError(t, err)
	logged := logs.String()
	require.Contains(t, logged, `"msg":"submission_accepted"`)
	require.Contains(t, logged, `"event":"submission_accepted"`)
	require.Contains(t, logged, `"reference_type":"submission"`)
	require.Contains(t, logged, `"reference_id":"`+ledger.result.IngestID+`"`)
	require.Contains(t, logged, `"correlation_id":"corr-canonical"`)
	require.Contains(t, logged, `"from":"none"`)
	require.Contains(t, logged, `"to":"queued"`)
	require.NotContains(t, logged, "private evidence")
}

func TestSubmissionWorkerLogsCompletedRetryAndTerminalOutcomesAfterPersistence(t *testing.T) {
	logger, logs := submissionLifecycleTestLogger()
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	service.logger = logger
	ledger.run.CorrelationID = "corr-worker"
	ledger.placement.CorrelationID = "corr-worker"

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Contains(t, logs.String(), `"msg":"submission_completed"`)
	require.Contains(t, logs.String(), `"stage":"semantic_commit"`)

	logs.Reset()
	nextAttemptAt := time.Date(2026, time.August, 18, 1, 2, 0, 0, time.UTC)
	service.assessments = &submissionLifecycleAssessmentStub{
		submissionAssessmentWorkerAssessmentStub: assessments,
		nextAttemptAt:                            &nextAttemptAt,
	}
	scope := submissionAssessmentRunScope(*ledger.run, service.workerID)
	require.NoError(t, service.retryOrFail(context.Background(), *ledger.run, scope, "assessment", true, false))
	retryLog := logs.String()
	require.Contains(t, retryLog, `"msg":"submission_retry_scheduled"`)
	require.Contains(t, retryLog, `"next_attempt_at":"2026-08-18T01:02:00Z"`)
	require.Contains(t, retryLog, `"attempts":1`)
	require.Contains(t, retryLog, `"max_attempts":3`)

	logs.Reset()
	require.NoError(t, service.completeRejected(context.Background(), scope, SubmissionErrorNoSupportedMemory, nil))
	failureLog := logs.String()
	require.Contains(t, failureLog, `"msg":"submission_rejected"`)
	require.Contains(t, failureLog, `"reason_code":"no_supported_memory"`)
	require.NotContains(t, failureLog, "resubmission_issues")

	logs.Reset()
	require.NoError(t, service.completeTerminalWithFailure(context.Background(), scope, "assessment", "timeout", 0, 0))
	failureLog = logs.String()
	require.Contains(t, failureLog, `"msg":"submission_failed"`)
	require.Contains(t, failureLog, `"reason_code":"provider_unavailable"`)
	require.NotContains(t, failureLog, "timeout")

	logs.Reset()
	preflight := deterministicSemanticAssessmentPreflightError("assessment_input", "input exceeds the configured token limit")
	require.NoError(t, service.completeTerminal(
		context.Background(), scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment_input", preflight,
	))
	classifiedFailureLog := logs.String()
	require.Contains(t, classifiedFailureLog, `"msg":"submission_failed"`)
	require.Contains(t, classifiedFailureLog, `"reason_code":"input_budget_exceeded"`)
}

func TestSubmissionWorkerLogsStaleSourceAsRejected(t *testing.T) {
	logger, logs := submissionLifecycleTestLogger()
	_, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	service.logger = logger
	assessments.commitErr = repository.ErrPlacementStaleSource

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Contains(t, logs.String(), `"msg":"submission_rejected"`)
	require.Contains(t, logs.String(), `"to":"rejected"`)
	require.Contains(t, logs.String(), `"reason_code":"stale_input"`)
	require.NotContains(t, logs.String(), `"msg":"submission_superseded"`)
	require.NotContains(t, logs.String(), `"level":"ERROR"`)
}

func TestSubmissionWorkerDoesNotLogTransitionWhenPersistenceFails(t *testing.T) {
	logger, logs := submissionLifecycleTestLogger()
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	service.logger = logger
	ledger.run.CorrelationID = "corr-failed-write"
	scope := submissionAssessmentRunScope(*ledger.run, service.workerID)
	lifecycleAssessments := &submissionLifecycleAssessmentStub{submissionAssessmentWorkerAssessmentStub: assessments}
	service.assessments = lifecycleAssessments

	lifecycleAssessments.requeueErr = errors.New("database detail must not be logged")
	err := service.retryOrFail(context.Background(), *ledger.run, scope, "assessment", false, false)
	require.Error(t, err)
	require.Empty(t, strings.TrimSpace(logs.String()))

	lifecycleAssessments.requeueErr = nil
	assessments.completeErr = errors.New("terminal write failed")
	err = service.completeTerminal(context.Background(), scope, string(domain.SemanticReviewTerminalFailure), "failed", "assessment")
	require.Error(t, err)
	require.Empty(t, strings.TrimSpace(logs.String()))
}

func submissionLifecycleTestLogger() (observability.LogProvider, *bytes.Buffer) {
	var logs bytes.Buffer
	handler := slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	return observability.NewWithHandler(handler), &logs
}

type submissionLifecycleAssessmentStub struct {
	*submissionAssessmentWorkerAssessmentStub
	requeueErr    error
	nextAttemptAt *time.Time
}

func (s *submissionLifecycleAssessmentStub) RequeueSubmissionAssessment(
	ctx context.Context,
	input repository.RequeueSubmissionAssessmentInput,
) (*repository.RequeueSubmissionAssessmentResult, error) {
	result, err := s.submissionAssessmentWorkerAssessmentStub.RequeueSubmissionAssessment(ctx, input)
	if err != nil {
		return nil, err
	}
	if s.requeueErr != nil {
		return nil, s.requeueErr
	}
	if result != nil {
		result.NextAttemptAt = s.nextAttemptAt
	}
	return result, nil
}
