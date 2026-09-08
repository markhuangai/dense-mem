package serverapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberFailureCodeMapsEmbeddingProviderResponseInvalid(t *testing.T) {
	failure := &rememberEmbeddingProviderFailure{cause: &embedding.ProviderError{
		FailureCode:  "provider_response_invalid",
		FailureClass: "provider_action_required",
	}}

	require.Equal(t, rememberapp.SubmissionErrorEmbeddingResponseInvalid, rememberFailureCode("embedding", failure))
}

func TestRememberFailureCodeMapsDuplicateCandidateStaleToStaleInput(t *testing.T) {
	require.Equal(t, rememberapp.SubmissionErrorStaleInput, rememberFailureCode("commit", repository.ErrRememberDuplicateCandidateStale))
	require.ErrorIs(t, normalizeRememberFailure(repository.ErrRememberDuplicateCandidateStale), rememberapp.ErrRememberStaleInput)
}

func TestRememberAssessmentSnapshotCarriesHashAndDuplicateEligibility(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "11111111-1111-4111-8111-111111111111", OwnerProfileID: "22222222-2222-4222-8222-222222222222",
		Evidence: []rememberapp.EvidenceInput{
			{Content: "normal", ContentHash: "sha256:normal"},
			{Content: "forced", ContentHash: "sha256:forced", ForceInsert: true},
			{Content: "versioned", ContentHash: "sha256:versioned", SourceKey: "doc://source", SourceRevisionToken: "rev-1"},
		},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "33333333-3333-4333-8333-333333333333")
	require.Equal(t, "sha256:normal", snapshot.Evidence[0].ContentHash)
	require.True(t, snapshot.Items[0].DuplicateAssessmentRequired)
	require.True(t, snapshot.Items[0].ExactReuseEligible)
	require.False(t, snapshot.Items[1].DuplicateAssessmentRequired)
	require.True(t, snapshot.Items[1].ExactReuseEligible)
	require.False(t, snapshot.Items[2].DuplicateAssessmentRequired)
	require.False(t, snapshot.Items[2].ExactReuseEligible)
}

func TestMergeInlineEmbeddingResultsDeduplicatesDocumentHashes(t *testing.T) {
	results := mergeInlineEmbeddingResults(
		[]repository.InlineEmbeddingResult{{DocumentHash: "same", Embedding: []float32{1}}},
		[]repository.InlineEmbeddingResult{{DocumentHash: "same", Embedding: []float32{2}}, {DocumentHash: "other", Embedding: []float32{3}}},
	)
	require.Len(t, results, 2)
	require.Equal(t, []float32{1}, results[0].Embedding)
	require.Equal(t, "other", results[1].DocumentHash)
}

func TestRememberFailureDiagnosticsCapturesBodiesAndRedactsSecrets(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[{"content":"safe"}],"authorization":"Bearer secret-token"}`)}
	publicResult := map[string]any{"processing_state": "failed", "errors": []any{map[string]any{"code": "provider_unavailable"}}}
	items := rememberFailureDiagnostics(input, publicResult, []modelprovider.ProviderExchange{{
		Component: "assessor", Model: "test-model", RequestBody: []byte(`{"messages":[{"content":"safe"}],"api_key":"secret-token"}`), ResponseBody: []byte(`{"error":{"message":"Authorization: Bearer sk-live-secret","stack_trace":"goroutine 1 [running]","database_error":"sql password=secret"}}`), StatusCode: 500, Outcome: "captured",
	}}, nil, true, "assessment")
	require.Len(t, items, 3)
	require.Equal(t, "original_request", items[0].Kind)
	require.Equal(t, "provider_exchange", items[1].Kind)
	require.Equal(t, "caller_response", items[2].Kind)
	require.NotContains(t, string(items[0].RequestBody), "secret-token")
	require.NotContains(t, string(items[1].RequestBody), "secret-token")
	require.NotContains(t, string(items[1].ResponseBody), "sk-live-secret")
	require.NotContains(t, string(items[1].ResponseBody), "goroutine 1")
	require.NotContains(t, string(items[1].ResponseBody), "sql password=secret")
	require.Contains(t, string(items[2].ResponseBody), `"isError":true`)
	require.Equal(t, "captured", items[1].Outcome)
	require.Equal(t, "captured", items[1].CaptureState)
	plain, _ := boundedRememberDiagnosticBody([]byte("api_key=plain-secret pq: password authentication failed for user dense"))
	require.NotContains(t, string(plain), "plain-secret")
	require.NotContains(t, string(plain), "password authentication failed")
	stack, _ := boundedRememberDiagnosticBody([]byte("goroutine 1 [running]:\nmain.main()\n\t/app/main.go:12\nprovider status"))
	require.NotContains(t, string(stack), "main.main")
	require.NotContains(t, string(stack), "/app/main.go")
	database, _ := boundedRememberDiagnosticBody([]byte("FATAL: password authentication failed for user dense"))
	require.NotContains(t, string(database), "password authentication failed")
	sqlState, _ := boundedRememberDiagnosticBody([]byte(`ERROR: duplicate key value violates unique constraint "accounts_pkey" (SQLSTATE 23505)`))
	require.NotContains(t, string(sqlState), "duplicate key value violates unique constraint")
	boundary, _ := boundedRememberDiagnosticBody(append([]byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes-20)), []byte(" api_key=boundary-secret")...))
	require.NotContains(t, string(boundary), "boundary-secret")
}

func TestRememberFailureDiagnosticsMarksUndeliveredCallerResponseOnCancellation(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[]}`)}
	items := rememberFailureDiagnostics(input, map[string]any{"processing_state": "failed"}, nil, []byte(`{"isError":true}`), false, "embedding")
	require.Len(t, items, 3)
	require.Equal(t, "not_delivered", items[2].Outcome)
	require.Equal(t, "not_delivered", items[2].CaptureState)
	require.Empty(t, items[2].ResponseBody)
}

func TestRememberFailureDiagnosticsUsesHashOnlyRequestForSecurityRejection(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		OriginalRequest:  []byte(`{"evidence":[{"content":"my production password is hunter2"}]}`),
		RequestHash:      "sha256:request-hash",
		SecurityRejected: true,
		Evidence:         []rememberapp.EvidenceInput{{Content: "my production password is hunter2"}},
	}
	items := rememberFailureDiagnostics(input, nil, nil, nil, true, "assessment")
	require.Equal(t, "hash_only", items[0].Outcome)
	require.Equal(t, "hash_only", items[0].CaptureState)
	require.Contains(t, string(items[0].RequestBody), "sha256:request-hash")
	require.NotContains(t, string(items[0].RequestBody), "hunter2")
}

func TestRememberCallerResponseDeliveryUsesRequestContext(t *testing.T) {
	require.True(t, rememberCallerResponseDelivered(context.Background(), rememberapp.ErrRememberRequestTimeout))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, rememberCallerResponseDelivered(ctx, rememberapp.ErrRememberRequestCancelled))
}

func TestRememberFailureDiagnosticsPreservesTruncationState(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes+1))}
	items := rememberFailureDiagnostics(input, nil, nil, nil, true, "assessment")
	require.Equal(t, "truncated", items[0].CaptureState)
	require.Len(t, items[0].RequestBody, rememberDiagnosticMaxBodyBytes)
}

func TestRememberExchangeRecorderBoundsBodiesAndAggregate(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "assessor", RequestBody: []byte("request"), ResponseBody: []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes+1)), Outcome: "captured",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Equal(t, "captured", exchanges[0].Outcome)
	require.Equal(t, "truncated", exchanges[0].CaptureState)
	require.LessOrEqual(t, len(exchanges[0].ResponseBody), rememberDiagnosticMaxBodyBytes)
}

func TestRememberExchangeRecorderProjectsProviderExchangeOnce(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component:    "embedding",
		RequestBody:  []byte(`{"model":"embedding-model","input":["private evidence"],"dimensions":2}`),
		ResponseBody: []byte(`{"model":"embedding-model","data":[{"index":0,"embedding":[0.1,0.2]}]}`),
		Outcome:      "captured",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Contains(t, string(exchanges[0].RequestBody), `"input_count":1`)
	require.Contains(t, string(exchanges[0].ResponseBody), `"embedding_dimensions":2`)
}

func TestRememberExchangeRecorderRetainsLaterMetadataAfterAggregateLimit(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	body := []byte(strings.Repeat("x", rememberDiagnosticMaxAttemptBytes/2))
	for index := 0; index < 3; index++ {
		recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
			Component: fmt.Sprintf("provider-%d", index), Model: "test-model", RequestBody: body,
			ResponseBody: body, StatusCode: 500 + index, Outcome: "captured",
		})
	}
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 3)
	require.Equal(t, "provider-2", exchanges[2].Component)
	require.Equal(t, 502, exchanges[2].StatusCode)
	require.Equal(t, "captured", exchanges[2].Outcome)
	require.Equal(t, "truncated", exchanges[2].CaptureState)
	require.Contains(t, string(exchanges[2].RequestBody), `"format":"non_json"`)
	require.Contains(t, string(exchanges[2].ResponseBody), `"format":"non_json"`)
}

func TestRememberFailureCodeMapsAssessmentDatabaseFailure(t *testing.T) {
	failure := errors.Join(rememberapp.ErrRememberDatabaseFailure, errors.New("catalog unavailable"))

	require.Equal(t, rememberapp.SubmissionErrorDatabaseFailure, rememberFailureCode("assessment", failure))
}

func TestRememberFailureRecoveryContextUsesPersistenceBudget(t *testing.T) {
	started := time.Now()
	ctx, cancel := rememberFailureRecoveryContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.InDelta(t, rememberapp.RememberFailurePersistenceBudget, deadline.Sub(started), float64(50*time.Millisecond))
}

func TestRememberReplayReloadUsesBoundedRecoveryContext(t *testing.T) {
	ledger := &rememberFailureLedgerStub{loadSequence: []*repository.RememberAttempt{{
		AttemptID: "77777777-7777-7777-7777-777777777777", Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}}}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, err := processor.loadRememberReplay(ctx, rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key",
	}, "88888888-8888-8888-8888-888888888888")
	require.NoError(t, err)
	require.Equal(t, "77777777-7777-7777-7777-777777777777", status.SubmissionID)
	require.Len(t, ledger.loadContexts, 1)
	require.NoError(t, ledger.loadContextErrors[0])
	require.InDelta(t, rememberapp.RememberFailurePersistenceBudget, time.Until(ledger.loadDeadlines[0]), float64(50*time.Millisecond))
}

func TestRememberReplayReloadFailurePreservesCompleteTypedResult(t *testing.T) {
	ledger := &rememberFailureLedgerStub{loadErr: errors.New("database unavailable")}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key",
		RequestHash: "request-hash", Metadata: map[string]any{"actor": map[string]any{"correlation_id": "replay-correlation"}},
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}, {Content: "second"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": "rel-a"}, {"ref": "rel-b"}}},
	}

	_, err := processor.loadRememberReplay(context.Background(), input, "88888888-8888-8888-8888-888888888888")
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, string(rememberapp.SubmissionErrorDatabaseFailure), processErr.Status.Errors[0].Code)
	require.Equal(t, "replay-correlation", processErr.Status.CorrelationID)
	require.Len(t, processErr.Status.Evidence, 2)
	require.Len(t, processErr.Status.RelationshipResults, 2)
}

func TestRememberAttemptMatchesRequestDoesNotAcceptMigratedHash(t *testing.T) {
	input := rememberapp.RememberProcessRequest{RequestHash: "current"}

	require.True(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{RequestHash: "current"}, input))
	require.False(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{
		RequestHash: "legacy", ContractVersion: "remember_request_hash_v1",
	}, input))
}

func TestRememberAttemptStatusForRequestRestoresRelationshipOrder(t *testing.T) {
	attempt := &repository.RememberAttempt{
		AttemptID: "77777777-7777-7777-7777-777777777777", Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"evidence": []any{}, "relationship_results": []any{
				map[string]any{"ref": "rel-a", "disposition": "stored", "splits": []any{}},
				map[string]any{"ref": "rel-b", "disposition": "stored", "splits": []any{}},
			}, "errors": []any{},
		},
	}
	status, err := rememberAttemptStatusForRequest(attempt, rememberapp.RememberProcessRequest{
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": "rel-b"}, {"ref": "rel-a"}}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"rel-b", "rel-a"}, []string{
		status.RelationshipResults[0].RelationshipRef,
		status.RelationshipResults[1].RelationshipRef,
	})
}

func TestRememberAttemptReplayRejectsLegacyOutcomes(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		RequestHash: "request-hash",
		Proposal:    map[string]any{"relationship_hints": []map[string]any{{"ref": "rel-a"}}},
	}
	publicResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
		"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
		"correlation_id": "correlation", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
	}
	for _, outcome := range []string{"rejected", "quarantined", "replayed"} {
		t.Run(outcome, func(t *testing.T) {
			_, err := rememberAttemptReplay(&repository.RememberAttempt{
				AttemptID: "77777777-7777-7777-7777-777777777777", ContractVersion: domain.ContractVersion,
				Outcome: outcome, PublicResult: publicResult,
			}, input)
			var processErr *rememberapp.RememberProcessError
			require.ErrorAs(t, err, &processErr)
			require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
			require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
		})
	}
}

func TestRememberAttemptReplayAcceptsOnlyCurrentTerminalOutcomes(t *testing.T) {
	input := rememberapp.RememberProcessRequest{RequestHash: "request-hash"}
	completed := &repository.RememberAttempt{
		AttemptID: "77777777-7777-7777-7777-777777777777", RequestHash: "request-hash", ContractVersion: domain.ContractVersion, Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"correlation_id": "correlation", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}
	status, err := rememberAttemptReplay(completed, input)
	require.NoError(t, err)
	require.Equal(t, "completed", status.ProcessingState)

	failed := *completed
	failed.Outcome = "failed"
	failed.PublicResult = map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": completed.AttemptID,
		"submission_kind": "remember", "processing_state": "failed", "search_state": "not_required",
		"correlation_id": "correlation", "evidence": []any{}, "relationship_results": []any{},
		"errors": []any{map[string]any{"code": "provider_unavailable", "retryable": true, "next_action": "retry_same_request", "message": "the semantic assessor was unavailable", "remediation": "Retry the same request with the same idempotency_key after the transient failure clears."}},
	}
	status, err = rememberAttemptReplay(&failed, input)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	require.Nil(t, status)
}

func TestRememberAttemptReplayRejectsRequestHashMismatch(t *testing.T) {
	attempt := &repository.RememberAttempt{
		AttemptID: "77777777-7777-7777-7777-777777777777", RequestHash: "stored-request-hash",
		ContractVersion: domain.ContractVersion, Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}
	_, err := rememberAttemptReplay(attempt, rememberapp.RememberProcessRequest{RequestHash: "different-request-hash"})
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
	require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
}

func TestRememberProcessorWaiterReplaysWithoutProcessing(t *testing.T) {
	base := &rememberFailureLedgerStub{load: &repository.RememberAttempt{
		AttemptID:       "77777777-7777-7777-7777-777777777777",
		RequestHash:     "request-hash",
		ContractVersion: domain.ContractVersion,
		Outcome:         "failed",
		Retryable:       true,
		PublicResult: map[string]any{
			"contract_version":     domain.ContractVersion,
			"submission_id":        "77777777-7777-7777-7777-777777777777",
			"submission_kind":      "remember",
			"processing_state":     "failed",
			"search_state":         "not_required",
			"evidence":             []any{},
			"relationship_results": []any{},
			"errors": []any{map[string]any{
				"code":        "provider_unavailable",
				"message":     "the semantic assessor was unavailable",
				"retryable":   true,
				"next_action": "retry_same_request",
				"remediation": "Retry the same request with the same idempotency_key after the transient failure clears.",
			}},
		},
	}}
	ledger := &rememberWaitAwareLedgerStub{rememberFailureLedgerStub: base, waited: true}
	processor := &rememberSynchronousProcessor{ledger: ledger}

	status, err := processor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
	})
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	require.NotNil(t, status)
	require.Equal(t, processErr.Status, status)
	require.Equal(t, "77777777-7777-7777-7777-777777777777", processErr.Status.SubmissionID)
	require.Equal(t, string(rememberapp.SubmissionErrorProviderUnavailable), processErr.Status.Errors[0].Code)
	require.Equal(t, 1, ledger.lockCalls)
	require.Len(t, base.loadContexts, 1)
	require.Empty(t, base.failure.Attempt.AttemptID, "a distributed waiter must not run a second processing attempt")
}

func TestRememberProcessorPreservesCompletedResultWhenLockCleanupFails(t *testing.T) {
	attemptID := "77777777-7777-7777-7777-777777777777"
	ledger := &rememberFailureLedgerStub{load: &repository.RememberAttempt{
		AttemptID: attemptID, RequestHash: "request-hash", ContractVersion: domain.ContractVersion, Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": attemptID,
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}}
	cleanupErr := errors.New("lock cleanup failed")
	logger := &rememberProcessorLogCapture{}
	locker := &rememberWaitAwareLedgerStub{rememberFailureLedgerStub: ledger, lockErr: cleanupErr}
	processor := &rememberSynchronousProcessor{ledger: locker, logger: logger}

	status, err := processor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
	})

	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, attemptID, status.SubmissionID)
	require.Equal(t, "completed", status.ProcessingState)
	require.Equal(t, []string{"remember_idempotency_lock_cleanup_failed"}, logger.warns)
}

func TestRememberProcessorWaiterRejectsRequestHashMismatch(t *testing.T) {
	ledger := &rememberFailureLedgerStub{load: &repository.RememberAttempt{
		AttemptID: "77777777-7777-7777-7777-777777777777", RequestHash: "stored-request-hash",
		ContractVersion: domain.ContractVersion, Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "77777777-7777-7777-7777-777777777777",
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}}
	locker := &rememberWaitAwareLedgerStub{rememberFailureLedgerStub: ledger, waited: true}
	processor := &rememberSynchronousProcessor{ledger: locker}

	status, err := processor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "different-request-hash",
	})

	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
	require.NotNil(t, status)
	require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
	require.Equal(t, processErr.Status, status)
}

func TestRememberProcessorWaiterLockCancellationReturnsBeforeReplayLoad(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	locker := &rememberWaitAwareLedgerStub{
		rememberFailureLedgerStub: ledger,
		waited:                    true,
		lockErr:                   context.DeadlineExceeded,
	}
	processor := &rememberSynchronousProcessor{ledger: locker}

	status, err := processor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
	})

	var processErr *rememberapp.RememberProcessError
	require.Nil(t, status)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorAs(t, err, &processErr)
	require.Empty(t, ledger.loadContexts, "a cancelled lock waiter must not load a replay")
}

func TestRememberProcessorRejectsScannerFailureBeforeAssessor(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "scanner-rejected", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "unsafe"}}, SecurityRejected: true,
	}

	_, err := processor.ProcessRemember(context.Background(), input)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPolicyRejected)
	require.Equal(t, string(rememberapp.SubmissionErrorPolicyRejected), processErr.Status.Errors[0].Code)
	require.Equal(t, "assessment", ledger.failure.Attempt.FailedPhase)
	require.False(t, ledger.failure.Attempt.Retryable)
}

func TestRememberProcessorFailureProjectsEverySubmittedItem(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "failure-correlation"}},
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}, {Content: "second"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": " rel-a "}, {"ref": "rel-b"}}},
	}

	_, err := processor.ProcessRemember(context.Background(), input)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, string(rememberapp.SubmissionErrorProviderUnavailable), processErr.Status.Errors[0].Code)
	require.Len(t, processErr.Status.Evidence, 2)
	require.Len(t, processErr.Status.RelationshipResults, 2)
	for index, evidence := range processErr.Status.Evidence {
		require.Equal(t, "not_stored", evidence.Disposition)
		require.Equal(t, index, evidence.EvidenceIndex)
		require.Equal(t, "internal_failure", evidence.Reason)
		require.Equal(t, "not_required", evidence.SearchState)
		require.Empty(t, evidence.SupersededEvidenceIDs)
	}
	for index, relationship := range processErr.Status.RelationshipResults {
		require.Equal(t, []string{"rel-a", "rel-b"}[index], relationship.RelationshipRef)
		require.Equal(t, "not_stored", relationship.Disposition)
		require.Equal(t, "internal_failure", relationship.Reason)
		require.Empty(t, relationship.Splits)
	}

	publicEvidence, ok := ledger.failure.Attempt.PublicResult["evidence"].([]any)
	require.True(t, ok)
	require.Len(t, publicEvidence, 2)
	publicRelationships, ok := ledger.failure.Attempt.PublicResult["relationship_results"].([]any)
	require.True(t, ok)
	require.Len(t, publicRelationships, 2)
}

func TestRememberProcessorInputBudgetUsesCanonicalTerminalGuidance(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")

	_, err := processor.recordRememberFailure(
		context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot,
		time.Now(), "assessment", 0, rememberapp.ErrRememberInputBudgetExceeded,
	)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	want := rememberapp.TerminalStatusError(rememberapp.TerminalErrorInputBudgetExceeded)
	want.ReasonCode = "remember_assessment_failed"
	want.Details = map[string]any{"component": "remember.assessment", "server_owned": true}
	want.NextAction = string(rememberapp.TerminalNextActionContactOperator)
	want.Remediation = "Ask an operator to review the configured assessor budget and server-owned context before retrying."
	require.Equal(t, want, processErr.Status.Errors[0])
	require.NoError(t, rememberapp.ValidateTerminalStatusError(processErr.Status.Errors[0]))
}

func TestRememberProcessorFailurePersistencePreservesDatabaseResult(t *testing.T) {
	ledger := &rememberFailureLedgerStub{failureErr: errors.New("failure record unavailable")}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}, {Content: "second"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": "rel-a"}, {"ref": "rel-b"}}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")

	_, err := processor.recordRememberFailure(
		context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot,
		time.Now(), "assessment", 0, rememberapp.ErrRememberProviderUnavailable,
	)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	want := rememberapp.TerminalStatusError(rememberapp.TerminalErrorDatabaseFailure)
	want.ReasonCode = "failure_retention"
	want.Details = map[string]any{"component": "remember.failure_record", "server_owned": true}
	require.Equal(t, want, processErr.Status.Errors[0])
	require.Len(t, processErr.Status.Evidence, 2)
	require.Len(t, processErr.Status.RelationshipResults, 2)
	for _, item := range processErr.Status.Evidence {
		require.Equal(t, "not_stored", item.Disposition)
		require.Equal(t, "internal_failure", item.Reason)
	}
	for _, item := range processErr.Status.RelationshipResults {
		require.Equal(t, "not_stored", item.Disposition)
		require.Equal(t, "internal_failure", item.Reason)
	}
}

func TestRememberProcessorRetentionDegradationPreservesCommittedFailure(t *testing.T) {
	ledger := &rememberFailureLedgerStub{failureErr: repository.ErrRememberFailureRetentionDegraded}
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{ledger: ledger, logger: logger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")

	_, err := processor.recordRememberFailure(
		context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot,
		time.Now(), "assessment", 0, rememberapp.ErrRememberProviderUnavailable,
	)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberProviderUnavailable)
	require.NotErrorIs(t, err, rememberapp.ErrRememberPersistence)
	require.Equal(t, string(rememberapp.SubmissionErrorProviderUnavailable), processErr.Status.Errors[0].Code)
	require.Contains(t, logger.warns, "remember_failure_retention_degraded")
}

func TestRememberProcessorConflictProjectsEverySubmittedItem(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "conflict-correlation"}},
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}, {Content: "second"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": " rel-a "}, {"ref": "rel-b"}}},
	}

	assertConflict := func(t *testing.T, ledger *rememberFailureLedgerStub) {
		t.Helper()
		processor := &rememberSynchronousProcessor{ledger: ledger}
		_, err := processor.ProcessRemember(context.Background(), input)
		var processErr *rememberapp.RememberProcessError
		require.ErrorAs(t, err, &processErr)
		require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
		require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
		require.Equal(t, "failed", processErr.Status.ProcessingState)
		require.Equal(t, "not_required", processErr.Status.SearchState)
		require.Equal(t, "conflict-correlation", processErr.Status.CorrelationID)
		require.NoError(t, func() error { _, err := uuid.Parse(processErr.Status.SubmissionID); return err }())
		require.Len(t, processErr.Status.Evidence, 2)
		require.Len(t, processErr.Status.RelationshipResults, 2)
		for index, evidence := range processErr.Status.Evidence {
			require.Equal(t, "not_stored", evidence.Disposition)
			require.Equal(t, index, evidence.EvidenceIndex)
			require.Equal(t, "internal_failure", evidence.Reason)
			require.Equal(t, "not_required", evidence.SearchState)
			require.Empty(t, evidence.SupersededEvidenceIDs)
		}
		for index, relationship := range processErr.Status.RelationshipResults {
			require.Equal(t, []string{"rel-a", "rel-b"}[index], relationship.RelationshipRef)
			require.Equal(t, "not_stored", relationship.Disposition)
			require.Equal(t, "internal_failure", relationship.Reason)
			require.Empty(t, relationship.Splits)
		}
	}

	t.Run("existing request mismatch", func(t *testing.T) {
		assertConflict(t, &rememberFailureLedgerStub{load: &repository.RememberAttempt{RequestHash: "different"}})
	})
	t.Run("persistence race", func(t *testing.T) {
		assertConflict(t, &rememberFailureLedgerStub{failureErr: repository.ErrIdempotencyConflict})
	})
}

type rememberFailureLedgerStub struct {
	failure           repository.RememberFailureRecordInput
	load              *repository.RememberAttempt
	loadErr           error
	failureErr        error
	loadSequence      []*repository.RememberAttempt
	loadContexts      []context.Context
	loadContextErrors []error
	loadDeadlines     []time.Time
}

type rememberWaitAwareLedgerStub struct {
	*rememberFailureLedgerStub
	waited    bool
	lockCalls int
	lockErr   error
}

func (s *rememberWaitAwareLedgerStub) WithRememberAttemptLock(_ context.Context, _, _, _ string, fn func(bool) error) error {
	s.lockCalls++
	callbackErr := fn(s.waited)
	return errors.Join(callbackErr, s.lockErr)
}

type rememberProcessorLogCapture struct {
	infos  []string
	warns  []string
	errors []string
}

func (l *rememberProcessorLogCapture) Info(message string, _ ...observability.LogAttr) {
	l.infos = append(l.infos, message)
}

func (l *rememberProcessorLogCapture) Error(message string, _ error, _ ...observability.LogAttr) {
	l.errors = append(l.errors, message)
}

func (l *rememberProcessorLogCapture) Warn(message string, _ ...observability.LogAttr) {
	l.warns = append(l.warns, message)
}

func (*rememberProcessorLogCapture) Debug(string, ...observability.LogAttr) {}

func (l *rememberProcessorLogCapture) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func (s *rememberFailureLedgerStub) LoadRememberAttempt(ctx context.Context, _ repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	s.loadContexts = append(s.loadContexts, ctx)
	s.loadContextErrors = append(s.loadContextErrors, ctx.Err())
	if deadline, ok := ctx.Deadline(); ok {
		s.loadDeadlines = append(s.loadDeadlines, deadline)
	}
	if len(s.loadSequence) > 0 {
		attempt := s.loadSequence[0]
		s.loadSequence = s.loadSequence[1:]
		return attempt, nil
	}
	if s.load != nil {
		return s.load, nil
	}
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return nil, repository.ErrRememberAttemptNotFound
}

func (*rememberFailureLedgerStub) PlanRememberEmbeddings(context.Context, repository.SynchronousRememberCommitInput) (*repository.InlineEmbeddingPlan, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) PlanRememberDuplicateEmbeddings(context.Context, repository.RememberDuplicateCandidateInput) (*repository.RememberDuplicateEmbeddingPlan, error) {
	return &repository.RememberDuplicateEmbeddingPlan{}, nil
}

func (s *rememberFailureLedgerStub) ResolveRememberDuplicateCandidates(_ context.Context, input repository.RememberDuplicateCandidateInput, _ []repository.InlineEmbeddingResult) (*repository.RememberDuplicateResolutionResult, error) {
	result := &repository.RememberDuplicateResolutionResult{Exact: make([]repository.RememberDuplicateResolution, len(input.Evidence))}
	for index, evidence := range input.Evidence {
		result.Exact[index] = repository.RememberDuplicateResolution{EvidenceIndex: index, EvidenceID: fmt.Sprintf("evidence:%d", index), InputFragmentID: evidence.FragmentID}
	}
	return result, nil
}

func (*rememberFailureLedgerStub) CommitRememberWithEmbeddings(context.Context, repository.SynchronousRememberCommitInput, []repository.InlineEmbeddingResult) (*repository.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (s *rememberFailureLedgerStub) RecordRememberFailure(_ context.Context, input repository.RememberFailureRecordInput) error {
	s.failure = input
	return s.failureErr
}
