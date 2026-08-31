package serverapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

func TestRememberFailureRecoveryContextUsesPersistenceBudget(t *testing.T) {
	started := time.Now()
	ctx, cancel := rememberFailureRecoveryContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.InDelta(t, rememberapp.RememberFailurePersistenceBudget, deadline.Sub(started), float64(50*time.Millisecond))
}

func TestRememberAttemptMatchesRequestDoesNotAcceptMigratedHash(t *testing.T) {
	input := rememberapp.RememberProcessRequest{RequestHash: "current"}

	require.True(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{RequestHash: "current"}, input))
	require.False(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{
		RequestHash: "legacy", ContractVersion: "remember_request_hash_v1",
	}, input))
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

type rememberFailureLedgerStub struct {
	failure repository.RememberFailureRecordInput
}

func (s *rememberFailureLedgerStub) LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	return nil, repository.ErrRememberAttemptNotFound
}

func (*rememberFailureLedgerStub) CommitRememberPreflightQuarantine(context.Context, repository.SynchronousRememberCommitInput, string) (*repository.SynchronousRememberCommitResult, error) {
	return nil, errors.New("unused")
}

func (*rememberFailureLedgerStub) CommitRememberTerminal(context.Context, repository.SynchronousRememberCommitInput, string, string, []repository.SubmissionAssessmentSecurityQuarantineInput) (*repository.SynchronousRememberCommitResult, error) {
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
	return nil
}
