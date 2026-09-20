package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberFailureCodeMapsEmbeddingProviderResponseInvalid(t *testing.T) {
	failure := &rememberEmbeddingProviderFailure{cause: &embeddingcontract.ProviderError{
		FailureCode:  "provider_response_invalid",
		FailureClass: "provider_action_required",
	}}

	require.Equal(t, rememberapp.SubmissionErrorEmbeddingResponseInvalid, rememberFailureCode("embedding", failure))
}

func TestRememberFailureCodeMapsDuplicateCandidateStaleToStaleInput(t *testing.T) {
	require.Equal(t, rememberapp.SubmissionErrorStaleInput, rememberFailureCode("commit", knowledgecontract.ErrRememberDuplicateCandidateStale))
	require.ErrorIs(t, normalizeRememberFailure(knowledgecontract.ErrRememberDuplicateCandidateStale), rememberapp.ErrRememberStaleInput)
}

func TestNormalizeRememberFailurePreservesAdapterStaleCause(t *testing.T) {
	processor := &rememberSynchronousProcessor{isStaleInput: rememberapp.IsRememberStaleInputError}
	for _, stale := range []error{
		knowledgepostgres.ErrConflictContextStale,
		knowledgepostgres.ErrRememberExactReferenceStale,
		knowledgepostgres.ErrCorrectionTargetStale,
	} {
		normalized := processor.normalizeRememberFailure(stale)
		require.ErrorIs(t, normalized, rememberapp.ErrRememberStaleInput)
		require.ErrorIs(t, normalized, stale)
		require.NotContains(t, normalized.Error(), stale.Error())
	}
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
		[]knowledgecontract.InlineEmbeddingResult{{DocumentHash: "same", Embedding: []float32{1}}},
		[]knowledgecontract.InlineEmbeddingResult{{DocumentHash: "same", Embedding: []float32{2}}, {DocumentHash: "other", Embedding: []float32{3}}},
	)
	require.Len(t, results, 2)
	require.Equal(t, []float32{1}, results[0].Embedding)
	require.Equal(t, "other", results[1].DocumentHash)
}

func TestRememberFailureDiagnosticsCapturesAdmittedBodiesAndProtectsConfiguredSecrets(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[{"content":"safe"}],"authorization":"Bearer secret-token"}`)}
	publicResult := map[string]any{"processing_state": "failed", "errors": []any{map[string]any{"code": "provider_unavailable"}}}
	callerResponse := []byte(`{"isError":true}`)
	protector := observability.NewCredentialProtector("secret-token", "sk-live-secret", "boundary-secret")
	items := rememberFailureDiagnostics(input, publicResult, []modelprovider.ProviderExchange{{
		Component: "assessor", Model: "test-model", RequestBody: []byte(`{"messages":[{"content":"safe"}],"api_key":"secret-token"}`), ResponseBody: []byte(`{"error":{"message":"Authorization: Bearer sk-live-secret","stack_trace":"goroutine 1 [running]","database_error":"sql password=secret"}}`), StatusCode: 500, Outcome: "captured",
	}}, callerResponse, true, "assessment", protector)
	require.Len(t, items, 3)
	require.Equal(t, "original_request", items[0].Kind)
	require.Equal(t, "provider_exchange", items[1].Kind)
	require.Equal(t, "caller_response", items[2].Kind)
	require.NotContains(t, string(items[0].RequestBody), "secret-token")
	require.NotContains(t, string(items[1].RequestBody), "secret-token")
	require.NotContains(t, string(items[1].ResponseBody), "sk-live-secret")
	require.Contains(t, string(items[1].ResponseBody), "goroutine 1")
	require.Contains(t, string(items[1].ResponseBody), "sql password=secret")
	require.Contains(t, string(items[2].ResponseBody), `"isError":true`)
	require.Equal(t, "captured", items[1].Outcome)
	require.Equal(t, "captured", items[1].CaptureState)
	plain, _ := boundedRememberDiagnosticBody([]byte("api_key=plain-secret pq: password authentication failed for user dense"))
	require.Contains(t, string(plain), "plain-secret")
	require.Contains(t, string(plain), "password authentication failed")
	stack, _ := boundedRememberDiagnosticBody([]byte("goroutine 1 [running]:\nmain.main()\n\t/app/main.go:12\nprovider status"))
	require.Contains(t, string(stack), "main.main")
	require.Contains(t, string(stack), "/app/main.go")
	database, _ := boundedRememberDiagnosticBody([]byte("FATAL: password authentication failed for user dense"))
	require.Contains(t, string(database), "password authentication failed")
	sqlState, _ := boundedRememberDiagnosticBody([]byte(`ERROR: duplicate key value violates unique constraint "accounts_pkey" (SQLSTATE 23505)`))
	require.Contains(t, string(sqlState), "duplicate key value violates unique constraint")
	boundary, _ := boundedRememberDiagnosticBody(append([]byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes-20)), []byte(" api_key=boundary-secret")...))
	protectedBoundary, _ := protectedRememberDiagnosticBody(boundary, protector)
	require.NotContains(t, string(protectedBoundary), "boundary-secret")
}

func TestRememberFailureDiagnosticsMarksUndeliveredCallerResponseOnCancellation(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[]}`)}
	items := rememberFailureDiagnostics(input, map[string]any{"processing_state": "failed"}, nil, []byte(`{"isError":true}`), false, "embedding")
	require.Len(t, items, 3)
	require.Equal(t, "not_delivered", items[2].Outcome)
	require.Equal(t, "not_delivered", items[2].CaptureState)
	require.Empty(t, items[2].ResponseBody)
}

func TestRememberFailureDiagnosticsDerivesProviderOutcomeCaptureState(t *testing.T) {
	for _, test := range []struct {
		outcome string
		want    string
	}{
		{outcome: "no_response", want: "no_response"},
		{outcome: "response_read_failed", want: "interrupted"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			items := rememberFailureDiagnosticsWithCapture(
				rememberapp.RememberProcessRequest{}, nil,
				[]modelprovider.ProviderExchange{{
					Component: "assessor", RequestBody: []byte(`{"request":true}`),
					Outcome: test.outcome, CaptureState: "captured",
				}},
				nil, true, false, observability.NewCredentialProtector(),
			)
			require.Len(t, items, 3)
			require.Equal(t, test.want, items[1].CaptureState)
		})
	}
}

func TestRememberInvocationDiagnosticsRecordOutcomeAndCause(t *testing.T) {
	started := time.Now().UTC().Add(-time.Second)
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger, logger: observability.New(0)}
	status := &rememberapp.SubmissionStatusResult{ProcessingState: "completed", Evidence: []rememberapp.SubmissionEvidenceStatus{{EvidenceIndex: 0}}}
	processor.recordRememberInvocation(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "11111111-1111-4111-8111-111111111111", OwnerProfileID: "22222222-2222-4222-8222-222222222222",
		InvocationStartedAt: started, RequestHash: "sha256:request", OriginalRequest: []byte(`{"evidence":[{"content":"admitted"}]}`),
	}, "33333333-3333-4333-8333-333333333333", "execution", "33333333-3333-4333-8333-333333333333", "commit", nil, status, nil)
	require.Equal(t, "evaluated_zero", ledger.invocation.Outcome)
	require.Empty(t, ledger.invocation.FailedPhase)
	require.Equal(t, "sha256:request", ledger.invocation.RequestHash)
	require.WithinDuration(t, started, ledger.invocation.CreatedAt, 50*time.Millisecond)
	require.GreaterOrEqual(t, ledger.invocation.Duration, time.Second)
}

func TestRememberInvocationDiagnosticsClassifiesRememberCancellationSentinels(t *testing.T) {
	for _, cause := range []error{rememberapp.ErrRememberRequestCancelled, rememberapp.ErrRememberRequestTimeout} {
		t.Run(cause.Error(), func(t *testing.T) {
			ledger := &rememberFailureLedgerStub{}
			processor := &rememberSynchronousProcessor{ledger: ledger}
			processor.recordRememberInvocation(context.Background(), rememberapp.RememberProcessRequest{
				TeamID: "11111111-1111-4111-8111-111111111111", OwnerProfileID: "22222222-2222-4222-8222-222222222222",
				RequestHash: "sha256:sentinel", InvocationStartedAt: time.Now().UTC(), OriginalRequest: []byte(`{"evidence":[]}`),
			}, "33333333-3333-4333-8333-333333333333", "execution", "", "embedding", fmt.Errorf("phase failed: %w", cause), nil, nil)

			require.Equal(t, "cancelled", ledger.invocation.Outcome)
			require.Equal(t, "embedding", ledger.invocation.FailedPhase)
		})
	}
}

func TestRememberInvocationLoggingPreservesFailureCauseThroughFallbackLogger(t *testing.T) {
	ledger := &rememberFailureLedgerStub{invocationErr: errors.New("diagnostic write failed")}
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{ledger: ledger, logger: logger}
	processor.recordRememberInvocation(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "11111111-1111-4111-8111-111111111111", OwnerProfileID: "22222222-2222-4222-8222-222222222222",
		RequestHash: "sha256:busy", InvocationStartedAt: time.Now().UTC(), OriginalRequest: []byte(`{"evidence":[]}`),
	}, "33333333-3333-4333-8333-333333333333", "execution", "", "idempotency_lock", errors.New("lock busy"), nil, nil)
	require.Equal(t, "failed", ledger.invocation.Outcome)
	require.Equal(t, "database_failure", ledger.invocation.ErrorCode)
	require.Equal(t, []string{"remember_invocation_diagnostic_unavailable"}, logger.warns)
	require.Equal(t, []string{"remember_invocation_completed"}, logger.errors)
}

func TestRememberFailureResultHelpersHandleNilAndTerminalStatus(t *testing.T) {
	public, terminal := terminalRememberFailureResult(nil)
	require.Empty(t, public)
	require.Nil(t, terminal)
	public, terminal = terminalRememberFailureResult(&rememberapp.SubmissionStatusResult{ProcessingState: "failed"})
	require.Equal(t, "failed", public["processing_state"])
	require.Equal(t, rememberapp.ResultKindTerminal, terminal.Kind)
	fallback := errors.New("fallback")
	require.ErrorIs(t, processErrOrCause(fmt.Errorf("wrapped: %w", fallback), errors.New("unused")), fallback)
	require.Same(t, fallback, processErrOrCause(nil, fallback))
	require.Zero(t, rememberInvocationDuration(time.Time{}))
}

func TestRememberFailureDiagnosticsDoesNotFabricateInternalCallerResponse(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[]}`)}
	items := rememberFailureDiagnosticsWithCapture(input, map[string]any{"processing_state": "failed"}, nil, nil, true, false)
	require.Len(t, items, 3)
	require.Equal(t, "not_captured", items[2].Outcome)
	require.Equal(t, "not_captured", items[2].CaptureState)
	require.Empty(t, items[2].ResponseBody)
}

type unavailableDiagnosticProtector struct{}

func (unavailableDiagnosticProtector) ProtectDiagnosticBytes([]byte, int, ...string) ([]byte, observability.CredentialProtectionUnavailableReason) {
	return nil, observability.CredentialProtectionBudgetExceeded
}

func TestRememberFailureDiagnosticsRetainsCredentialProtectionUnavailableState(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(`{"evidence":[{"content":"admitted"}]}`)}
	items := rememberFailureDiagnosticsWithCapture(
		input,
		map[string]any{"processing_state": "failed"},
		[]modelprovider.ProviderExchange{{
			Component: "assessor", RequestBody: []byte(`{"messages":[{"content":"admitted"}]}`),
			ResponseBody: []byte(`{"error":"provider unavailable"}`), Outcome: "captured",
		}},
		[]byte(`{"isError":true}`), true, true, unavailableDiagnosticProtector{},
	)
	require.Len(t, items, 3)
	require.Equal(t, "unavailable", items[0].CaptureState)
	require.Equal(t, "credential_protection_2", items[0].CaptureReason)
	require.Empty(t, items[0].RequestBody)
	require.Equal(t, "unavailable", items[1].CaptureState)
	require.Equal(t, "credential_protection_2", items[1].CaptureReason)
	require.Empty(t, items[1].RequestBody)
	require.Empty(t, items[1].ResponseBody)
	require.Equal(t, "unavailable", items[2].CaptureState)
	require.Equal(t, "credential_protection_2", items[2].CaptureReason)
}

func TestRememberFailureDiagnosticsUsesHashOnlyRequestForSecurityRejection(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		OriginalRequest:  []byte(`{"evidence":[{"content":"my production password is hunter2"}]}`),
		RequestHash:      "sha256:request-hash",
		SecurityRejected: true,
		Evidence:         []rememberapp.EvidenceInput{{Content: "my production password is hunter2"}},
	}
	items := rememberFailureDiagnostics(input, nil, nil, nil, true, "assessment", observability.NewCredentialProtector())
	require.Equal(t, "hash_only", items[0].Outcome)
	require.Equal(t, "hash_only", items[0].CaptureState)
	require.Contains(t, string(items[0].RequestBody), "sha256:request-hash")
	require.NotContains(t, string(items[0].RequestBody), "hunter2")
}

func TestRememberCallerResponseDeliveryUsesRequestContext(t *testing.T) {
	require.True(t, rememberCallerResponseDelivered(context.Background(), rememberapp.ErrRememberRequestTimeout))
	require.True(t, rememberCallerResponseDelivered(context.Background(), context.DeadlineExceeded))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, rememberCallerResponseDelivered(ctx, rememberapp.ErrRememberRequestCancelled))
	requestCtx := context.Background()
	require.True(t, rememberCallerResponseDelivered(
		rememberapp.WithCallerResponseRequestContext(context.Background(), requestCtx),
		rememberapp.ErrRememberRequestTimeout,
	))
	requestCtx, requestCancel := context.WithCancel(context.Background())
	requestCancel()
	require.False(t, rememberCallerResponseDelivered(
		rememberapp.WithCallerResponseRequestContext(context.Background(), requestCtx),
		rememberapp.ErrRememberRequestTimeout,
	))
}

func TestRememberFailureDiagnosticsMarksOversizedRequestUnavailable(t *testing.T) {
	input := rememberapp.RememberProcessRequest{OriginalRequest: []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes+1))}
	items := rememberFailureDiagnostics(input, nil, nil, nil, true, "assessment", observability.NewCredentialProtector())
	require.Equal(t, "unavailable", items[0].CaptureState)
	require.Equal(t, "credential_protection_2", items[0].CaptureReason)
	require.Empty(t, items[0].RequestBody)
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
	ledger := &rememberFailureLedgerStub{loadSequence: []*knowledgecontract.RememberAttempt{{
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

	require.True(t, rememberAttemptMatchesRequest(&knowledgecontract.RememberAttempt{RequestHash: "current"}, input))
	require.False(t, rememberAttemptMatchesRequest(&knowledgecontract.RememberAttempt{
		RequestHash: "legacy", ContractVersion: "remember_request_hash_v1",
	}, input))
}

func TestRememberAttemptStatusForRequestRestoresRelationshipOrder(t *testing.T) {
	attempt := &knowledgecontract.RememberAttempt{
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
			_, err := rememberAttemptReplay(&knowledgecontract.RememberAttempt{
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
	completed := &knowledgecontract.RememberAttempt{
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
	attempt := &knowledgecontract.RememberAttempt{
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
	base := &rememberFailureLedgerStub{load: &knowledgecontract.RememberAttempt{
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
	require.Equal(t, "replay", base.invocation.Classification)
	require.Equal(t, "failed", base.invocation.Outcome)
	require.Equal(t, "77777777-7777-7777-7777-777777777777", base.invocation.CanonicalAttemptID)
}

func TestReplayCanonicalAttemptIDIgnoresSyntheticFailureStatus(t *testing.T) {
	require.Empty(t, replayCanonicalAttemptID(nil, "synthetic"))
	require.Empty(t, replayCanonicalAttemptID(&rememberapp.SubmissionStatusResult{SubmissionID: "synthetic"}, "synthetic"))
	require.Equal(t, "attempt", replayCanonicalAttemptID(&rememberapp.SubmissionStatusResult{SubmissionID: "attempt"}, "synthetic"))
}

func TestRememberProcessorPreservesCompletedResultWhenLockCleanupFails(t *testing.T) {
	attemptID := "77777777-7777-7777-7777-777777777777"
	ledger := &rememberFailureLedgerStub{load: &knowledgecontract.RememberAttempt{
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
	ledger := &rememberFailureLedgerStub{load: &knowledgecontract.RememberAttempt{
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
	require.Equal(t, "conflict", ledger.invocation.Classification)
	require.Equal(t, "conflict", ledger.invocation.Outcome)
	require.Empty(t, ledger.invocation.FailedPhase)
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
	require.Equal(t, "execution", ledger.invocation.Classification)
	require.Equal(t, "cancelled", ledger.invocation.Outcome)
	require.Equal(t, "request_timeout", ledger.invocation.ErrorCode)
	require.True(t, ledger.invocation.Retryable)
	require.Equal(t, "idempotency_wait", ledger.invocation.FailedPhase)
	require.Empty(t, ledger.invocation.CanonicalAttemptID)
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

func TestRememberProcessorPersistsBoundedAssessorValidationDiagnostics(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")
	_, _, err := processor.recordRememberFailure(context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot, time.Now(), "assessment", 3, &assessor.MalformedResponseError{
		FailureClass: "malformed_exhausted", Attempts: 3, ValidationStage: "response_contract",
		ValidationFieldFamilies: []string{"relationship_results[0].object_value", "unknown-secret-field"},
	})
	require.Error(t, err)
	require.NotNil(t, ledger.failure.Attempt.AssessorValidation)
	require.Equal(t, "malformed_exhausted", ledger.failure.Attempt.AssessorValidation["failure_class"])
	require.NotContains(t, ledger.failure.Attempt.AssessorValidation, "unknown-secret-field")
}

func TestRememberProcessorInputBudgetUsesCanonicalTerminalGuidance(t *testing.T) {
	ledger := &rememberFailureLedgerStub{}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")

	_, _, err := processor.recordRememberFailure(
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

	_, canonicalAttemptID, err := processor.recordRememberFailure(
		context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot,
		time.Now(), "assessment", 0, rememberapp.ErrRememberProviderUnavailable,
	)
	require.Empty(t, canonicalAttemptID)
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
	ledger := &rememberFailureLedgerStub{failureErr: knowledgecontract.ErrRememberFailureRetentionDegraded}
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{ledger: ledger, logger: logger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "remember-key", RequestHash: "request-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "first"}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "88888888-8888-8888-8888-888888888888")

	_, canonicalAttemptID, err := processor.recordRememberFailure(
		context.Background(), input, "88888888-8888-8888-8888-888888888888", snapshot,
		time.Now(), "assessment", 0, rememberapp.ErrRememberProviderUnavailable,
	)
	require.Equal(t, "88888888-8888-8888-8888-888888888888", canonicalAttemptID)

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
		require.Equal(t, "conflict", ledger.invocation.Outcome)
		require.Equal(t, "conflict", ledger.invocation.Classification)
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
		assertConflict(t, &rememberFailureLedgerStub{load: &knowledgecontract.RememberAttempt{RequestHash: "different"}})
	})
	t.Run("persistence race", func(t *testing.T) {
		assertConflict(t, &rememberFailureLedgerStub{failureErr: knowledgecontract.ErrIdempotencyConflict})
	})
}

type rememberFailureLedgerStub struct {
	failure           knowledgecontract.RememberFailureRecordInput
	load              *knowledgecontract.RememberAttempt
	loadErr           error
	failureErr        error
	loadSequence      []*knowledgecontract.RememberAttempt
	loadContexts      []context.Context
	loadContextErrors []error
	loadDeadlines     []time.Time
	invocation        knowledgecontract.RememberInvocationDiagnosticInput
	invocations       []knowledgecontract.RememberInvocationDiagnosticInput
	invocationErr     error
}

type rememberWaitAwareLedgerStub struct {
	*rememberFailureLedgerStub
	waited       bool
	lockCalls    int
	lockErr      error
	skipCallback bool
}

func (s *rememberWaitAwareLedgerStub) WithRememberAttemptLock(_ context.Context, _, _, _ string, fn func(bool) error) error {
	s.lockCalls++
	if s.skipCallback {
		return s.lockErr
	}
	callbackErr := fn(s.waited)
	return errors.Join(callbackErr, s.lockErr)
}

type rememberProcessorLogCapture struct {
	infos      []string
	warns      []string
	warnTexts  []string
	errors     []string
	errorTexts []string
}

func (l *rememberProcessorLogCapture) Info(message string, _ ...observability.LogAttr) {
	l.infos = append(l.infos, message)
}

func (l *rememberProcessorLogCapture) Error(message string, err error, _ ...observability.LogAttr) {
	l.errors = append(l.errors, message)
	if err != nil {
		l.errorTexts = append(l.errorTexts, err.Error())
	}
}

func (l *rememberProcessorLogCapture) Warn(message string, attrs ...observability.LogAttr) {
	l.warns = append(l.warns, message)
	for _, attr := range attrs {
		if attr.Key == "error" {
			l.warnTexts = append(l.warnTexts, fmt.Sprint(attr.Value))
		}
	}
}

func (*rememberProcessorLogCapture) Debug(string, ...observability.LogAttr) {}

func (l *rememberProcessorLogCapture) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func (s *rememberFailureLedgerStub) LoadRememberAttempt(ctx context.Context, _ knowledgecontract.RememberAttemptLookupInput) (*knowledgecontract.RememberAttempt, error) {
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
	return nil, knowledgecontract.ErrRememberAttemptNotFound
}

func (*rememberFailureLedgerStub) PlanRememberEmbeddings(context.Context, knowledgecontract.SynchronousRememberCommitInput) (*knowledgecontract.InlineEmbeddingPlan, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) PlanRememberDuplicateEmbeddings(context.Context, knowledgecontract.RememberDuplicateCandidateInput) (*knowledgecontract.RememberDuplicateEmbeddingPlan, error) {
	return &knowledgecontract.RememberDuplicateEmbeddingPlan{}, nil
}

func (s *rememberFailureLedgerStub) ResolveRememberDuplicateCandidates(_ context.Context, input knowledgecontract.RememberDuplicateCandidateInput, _ []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.RememberDuplicateResolutionResult, error) {
	result := &knowledgecontract.RememberDuplicateResolutionResult{Exact: make([]knowledgecontract.RememberDuplicateResolution, len(input.Evidence))}
	for index, evidence := range input.Evidence {
		result.Exact[index] = knowledgecontract.RememberDuplicateResolution{EvidenceIndex: index, EvidenceID: fmt.Sprintf("evidence:%d", index), InputFragmentID: evidence.FragmentID}
	}
	return result, nil
}

func (*rememberFailureLedgerStub) CommitRememberWithEmbeddings(context.Context, knowledgecontract.SynchronousRememberCommitInput, []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (s *rememberFailureLedgerStub) RecordRememberFailure(_ context.Context, input knowledgecontract.RememberFailureRecordInput) error {
	s.failure = input
	return s.failureErr
}

func (s *rememberFailureLedgerStub) RecordRememberInvocationDiagnostic(_ context.Context, input knowledgecontract.RememberInvocationDiagnosticInput) error {
	s.invocation = input
	s.invocations = append(s.invocations, input)
	return s.invocationErr
}
