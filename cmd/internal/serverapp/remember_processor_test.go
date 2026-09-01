package serverapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
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

func TestRememberFailureCodeMapsAssessmentDatabaseFailure(t *testing.T) {
	failure := errors.Join(rememberapp.ErrRememberDatabaseFailure, errors.New("catalog unavailable"))

	require.Equal(t, rememberapp.SubmissionErrorDatabaseFailure, rememberFailureCode("assessment", failure))
}

func TestRememberTerminalErrorInputUsesCanonicalStatus(t *testing.T) {
	for _, code := range []rememberapp.TerminalErrorCode{
		rememberapp.TerminalErrorNoSupportedMemory,
		rememberapp.TerminalErrorQuarantined,
	} {
		want := rememberapp.TerminalStatusError(code)
		got := rememberTerminalErrorInput(code)
		require.Equal(t, want.Code, got.Code)
		require.Equal(t, want.Message, got.Message)
		require.Equal(t, want.Retryable, got.Retryable)
		require.Equal(t, want.NextAction, got.NextAction)
		require.Equal(t, want.Remediation, got.Remediation)
	}
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

func (*rememberFailureLedgerStub) CommitRememberPreflightQuarantine(context.Context, repository.SynchronousRememberCommitInput, repository.RememberTerminalErrorInput) (*repository.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) CommitRememberTerminal(context.Context, repository.SynchronousRememberCommitInput, string, repository.RememberTerminalErrorInput, []repository.SubmissionAssessmentSecurityQuarantineInput) (*repository.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) PlanRememberEmbeddings(context.Context, repository.SynchronousRememberCommitInput) (*repository.InlineEmbeddingPlan, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) CommitRememberWithEmbeddings(context.Context, repository.SynchronousRememberCommitInput, []repository.InlineEmbeddingResult) (*repository.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (s *rememberFailureLedgerStub) RecordRememberFailure(_ context.Context, input repository.RememberFailureRecordInput) error {
	s.failure = input
	return s.failureErr
}
