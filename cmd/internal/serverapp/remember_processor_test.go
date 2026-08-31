package serverapp

import (
	"context"
	"errors"
	"testing"

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
