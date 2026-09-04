package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
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

func TestRememberFailureRequestArtifactIsCanonicalAndRedacted(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		RequestHash:    "sha256:" + strings.Repeat("a", 64),
		IdempotencyKey: "client-key-that-must-not-be-retained",
		Proposal: map[string]any{
			"entity_hints": []map[string]any{{"name": "private entity name", "entity_kind": "project"}},
			"relationship_hints": []map[string]any{{
				"ref":              "relationship-ref",
				"evidence_indices": []any{1, 0, 1, 99},
				"subject":          map[string]any{"name": "private subject", "entity_kind": "project"},
				"predicate":        map[string]any{"proposed_key": "uses"},
				"object":           map[string]any{"value": map[string]any{"type": "string", "value": "private object", "display": "private display", "unit": "private unit"}},
				"polarity":         "+",
				"client_comment":   "provider prompt and secret should not be retained",
				"metadata":         map[string]any{"authorization": "Bearer secret-token"},
			}},
			"provider_response": "raw provider response must not be retained",
		},
	}
	evidence := []repository.EvidenceFragment{{EvidenceIndex: 0, Content: "evidence content with secret-token"}}

	first, ok := rememberFailureRequestArtifact(input, "11111111-1111-4111-8111-111111111111", evidence)
	require.True(t, ok)
	second, ok := rememberFailureRequestArtifact(input, "11111111-1111-4111-8111-111111111111", evidence)
	require.True(t, ok)
	require.True(t, bytes.Equal(first.Content, second.Content), "artifact JSON must be deterministic")
	require.LessOrEqual(t, len(first.Content), rememberFailureArtifactMaxBytes)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(first.Content, &payload))
	require.NotContains(t, string(first.Content), evidence[0].Content)
	for _, secret := range []string{"client-key-that-must-not-be-retained", "provider prompt", "secret-token", "private entity name", "private subject", "private object", "private display", "private unit", "authorization", "provider_response", "client_comment", "metadata"} {
		require.NotContains(t, string(first.Content), secret)
	}
	require.Equal(t, input.RequestHash, payload["request_hash"])
	require.NotEmpty(t, payload["idempotency_key_hash"])
	require.Len(t, payload["evidence"], 1)
	require.Len(t, payload["relationships"], 1)
	relationship := payload["relationships"].([]any)[0].(map[string]any)
	require.Equal(t, []any{float64(0), float64(1)}, relationship["evidence_indices"])
	require.NotEmpty(t, relationship["ref_hash"])
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
