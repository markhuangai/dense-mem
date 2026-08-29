package synchronousremember

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousTerminalOutcomeUsesClosedNonStoredReasons(t *testing.T) {
	input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "evidence"}}, Metadata: map[string]any{"actor": map[string]any{"correlation_id": "synchronous-test-correlation"}}}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{EvidenceIndex: 0}}}

	for _, test := range []struct {
		outcome string
		reason  string
	}{{"rejected", "not_supported_by_evidence"}, {"quarantined", "security_quarantine"}} {
		result := synchronousTerminalOutcome(input, created, []string{"relationship"}, test.outcome, nil)
		if result.Evidence[0].Reason != test.reason || result.RelationshipResults[0].Reason != test.reason {
			t.Fatalf("%s reasons = %q / %q, want %q", test.outcome, result.Evidence[0].Reason, result.RelationshipResults[0].Reason, test.reason)
		}
		if len(result.Evidence[0].SupersededEvidenceIDs) != 0 {
			t.Fatalf("%s non-stored evidence retained supersession IDs: %v", test.outcome, result.Evidence[0].SupersededEvidenceIDs)
		}
		if err := remember.ValidateTerminalRememberResult(result, 1, []string{"relationship"}); err != nil {
			t.Fatalf("%s terminal result is invalid: %v", test.outcome, err)
		}
	}
}

func TestSynchronousEvidencePreservesInitialSecurityEvent(t *testing.T) {
	input := []remember.EvidenceInput{{
		Content: "evidence", ContentHash: "hash", SupersedesEvidenceIDs: []string{"target"},
		InitialEvent: &remember.SecurityEventDraft{
			EventKind: "deterministic_scan", Decision: "pass", Reason: "scan passed",
			Signals:  []remember.SecuritySignalInput{{Kind: "instruction_override", Severity: "high", SpanStart: 1, SpanEnd: 2, Metadata: map[string]any{"rule_id": "rule"}}},
			Metadata: map[string]any{"source": "intake"},
		},
	}}

	converted := synchronousEvidence(input)

	require.Len(t, converted, 1)
	require.NotNil(t, converted[0].InitialEvent)
	require.Equal(t, "deterministic_scan", converted[0].InitialEvent.EventKind)
	require.Equal(t, "pass", converted[0].InitialEvent.Decision)
	require.Equal(t, "scan passed", converted[0].InitialEvent.Reason)
	require.Equal(t, map[string]any{"source": "intake"}, converted[0].InitialEvent.Metadata)
	require.Equal(t, []repository.SecuritySignalInput{{Kind: "instruction_override", Severity: "high", SpanStart: 1, SpanEnd: 2, Metadata: map[string]any{"rule_id": "rule"}}}, converted[0].InitialEvent.Signals)
}

func TestSynchronousCompletedResultIncludesCommittedSupersessionIDs(t *testing.T) {
	input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "evidence"}}, Metadata: map[string]any{"actor": map[string]any{"correlation_id": "synchronous-test-correlation"}}}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0, SupersededEvidenceIDs: []string{uuid.NewString()}}}}

	result := synchronousCompletedResult(input, created, &repository.CommitSubmissionAssessmentResult{}, nil, nil, nil)

	require.Equal(t, created.Evidence[0].SupersededEvidenceIDs, result.Evidence[0].SupersededEvidenceIDs)
}

func TestSynchronousCompletedOutcomeUsesEmptySupersededEvidenceArray(t *testing.T) {
	input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "evidence"}}, Metadata: map[string]any{"actor": map[string]any{"correlation_id": "synchronous-test-correlation"}}}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0}}}

	result := synchronousCompletedResult(input, created, &repository.CommitSubmissionAssessmentResult{}, nil, nil, nil)
	if err := remember.ValidateTerminalRememberResult(result, 1, nil); err != nil {
		t.Fatalf("completed terminal result is invalid: %v", err)
	}
}

func TestSynchronousCompletedOutcomeKeepsStoredRelationshipProjection(t *testing.T) {
	input := remember.RememberProcessRequest{
		Evidence: []remember.EvidenceInput{{Content: "evidence"}},
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "synchronous-test-correlation"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{"ref": "durable-store"}}},
	}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0}}}
	committed := &repository.CommitSubmissionAssessmentResult{RelationshipResults: []repository.RelationshipDecisionResult{{
		ProposalID:   "durable-store",
		Relationship: &repository.RelationshipRecord{RelationshipID: uuid.NewString(), Version: 1, Status: "active"},
	}}}
	observations := []repository.SubmissionAssessmentRelationshipObservationInput{{
		RelationshipRef: "durable-store", Observation: repository.PlacementRelationshipDecisionInput{Ref: "durable-store"},
	}}
	result := synchronousCompletedResult(input, created, committed, []repository.SubmissionRelationshipResultInput{{RelationshipRef: "durable-store", Disposition: "stored"}}, observations, []string{"durable-store"})
	if err := remember.ValidateTerminalRememberResult(result, 1, []string{"durable-store"}); err != nil {
		t.Fatalf("stored terminal result is invalid: %v", err)
	}
}

func TestSynchronousFailureClassifiesMalformedAssessmentResponse(t *testing.T) {
	err := &assessor.MalformedResponseError{Provider: "test", FailureClass: "malformed_exhausted"}
	if got := synchronousFailureCode("assessment", err); got != remember.TerminalErrorProviderResponseInvalid {
		t.Fatalf("failure code = %q, want %q", got, remember.TerminalErrorProviderResponseInvalid)
	}
}

func TestSynchronousAssessmentFailurePreservesTurnsAndElapsedDuration(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousMalformedProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-malformed-exhausted", "pipeline-malformed-exhausted-hash"))
	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, 3, ledger.failureInput.Attempt.AssessorTurns)
	require.GreaterOrEqual(t, ledger.failureInput.Attempt.Duration, 20*time.Millisecond)
}

func TestSynchronousFailureDistinguishesProviderUnavailabilityFromRequestDeadline(t *testing.T) {
	if got := synchronousFailureCode("embedding", semanticwrite.ErrProviderUnavailable); got != remember.TerminalErrorEmbeddingUnavailable {
		t.Fatalf("embedding provider failure code = %q, want %q", got, remember.TerminalErrorEmbeddingUnavailable)
	}
	if got := synchronousFailureCode("assessment", errors.New("assessor provider timed out")); got != remember.TerminalErrorProviderUnavailable {
		t.Fatalf("assessor provider failure code = %q, want %q", got, remember.TerminalErrorProviderUnavailable)
	}
	if got := synchronousFailureCode("embedding", context.DeadlineExceeded); got != remember.TerminalErrorRequestTimeout {
		t.Fatalf("embedding phase deadline code = %q, want %q", got, remember.TerminalErrorRequestTimeout)
	}
}

func TestSynchronousFailureClassifiesRepositoryDatabaseFailures(t *testing.T) {
	databaseErr := fmt.Errorf("repository query failed: %w", &pgconn.PgError{Code: "08006"})
	for _, phase := range []string{"embedding", "commit"} {
		require.Equal(t, remember.TerminalErrorDatabaseFailure, synchronousFailureCode(phase, databaseErr), phase)
	}
}

type synchronousRememberCommitStageError struct{}

func (synchronousRememberCommitStageError) Error() string { return "commit stage" }

func (synchronousRememberCommitStageError) SynchronousRememberCommitStage() string {
	return " commit-stage "
}

type synchronousRememberEmbeddingStageError struct{}

func (synchronousRememberEmbeddingStageError) Error() string { return "embedding stage" }

func (synchronousRememberEmbeddingStageError) SynchronousRememberEmbeddingStage() string {
	return " embedding-stage "
}

func TestSynchronousRememberClassifiersCoverTypedStagesAndTerminalErrors(t *testing.T) {
	if got := synchronousRememberCommitStage(synchronousRememberCommitStageError{}); got != "commit-stage" {
		t.Fatalf("commit stage = %q", got)
	}
	if got := synchronousRememberCommitStage(errors.New("plain")); got != "" {
		t.Fatalf("plain commit stage = %q", got)
	}
	if got := synchronousRememberEmbeddingStage(synchronousRememberEmbeddingStageError{}); got != "embedding-stage" {
		t.Fatalf("embedding stage = %q", got)
	}
	if got := synchronousRememberEmbeddingStage(errors.New("plain")); got != "" {
		t.Fatalf("plain embedding stage = %q", got)
	}

	for _, test := range []struct {
		phase string
		err   error
		want  remember.TerminalErrorCode
	}{
		{phase: "assessment", err: context.Canceled, want: remember.TerminalErrorRequestCancelled},
		{phase: "commit", err: repository.ErrIdempotencyConflict, want: remember.TerminalErrorIdempotencyConflict},
		{phase: "commit", err: repository.ErrPlacementStaleSource, want: remember.TerminalErrorStaleInput},
		{phase: "embedding", err: repository.ErrSynchronousRememberEmbeddingInputBudget, want: remember.TerminalErrorInputBudgetExceeded},
		{phase: "embedding", err: repository.ErrSynchronousRememberEmbeddingFence, want: remember.TerminalErrorInternalFailure},
		{phase: "commit", err: repository.ErrSynchronousRememberEmbeddingFence, want: remember.TerminalErrorCommitConflict},
		{phase: "embedding", err: semanticwrite.ErrProviderResponseInvalid, want: remember.TerminalErrorEmbeddingResponseInvalid},
		{phase: "embedding", err: semanticwrite.ErrInvalidPlan, want: remember.TerminalErrorEmbeddingResponseInvalid},
		{phase: "assessment", err: assessor.ErrVerifierMalformedResponse, want: remember.TerminalErrorProviderResponseInvalid},
		{phase: "commit", err: errors.New("commit failed"), want: remember.TerminalErrorCommitConflict},
		{phase: "other", err: errors.New("internal failed"), want: remember.TerminalErrorInternalFailure},
	} {
		if got := synchronousFailureCode(test.phase, test.err); got != test.want {
			t.Errorf("%s / %v = %q, want %q", test.phase, test.err, got, test.want)
		}
	}
}

func TestSynchronousRememberHashMatchesRequiresMigrationContract(t *testing.T) {
	input := remember.RememberProcessRequest{RequestHash: "current", MigratedRequestHash: "migrated"}
	if !domain.RememberRequestHashMatches("current", domain.ContractVersion, input.RequestHash, input.MigratedRequestHash) {
		t.Fatal("current request hash must match")
	}
	if !domain.RememberRequestHashMatches("migrated", domain.MigratedRememberRequestHashVersion, input.RequestHash, input.MigratedRequestHash) {
		t.Fatal("recognized migration hash must match")
	}
	if domain.RememberRequestHashMatches("migrated", domain.ContractVersion, input.RequestHash, input.MigratedRequestHash) {
		t.Fatal("non-migrated attempt must not match a migration hash")
	}
}

func TestSynchronousFailureReplaysConcurrentTerminalWinner(t *testing.T) {
	input := remember.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "replay-key", RequestHash: "replay-hash",
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "winner-correlation"}},
	}
	winner := &remember.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: "winner-submission", SubmissionKind: "remember",
		ProcessingState: "completed", SearchState: "current", CorrelationID: "winner-correlation",
		Evidence: []remember.TerminalEvidenceResult{}, RelationshipResults: []remember.SubmissionRelationshipResult{},
		Errors: []remember.SubmissionStatusError{}, Kind: remember.ResultKindTerminal,
	}
	public, err := terminalMap(winner)
	require.NoError(t, err)
	ledger := &synchronousPipelineLedger{
		loadResult:       &repository.RememberAttempt{AttemptID: "winner-submission", RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public},
		recordFailureErr: repository.ErrRememberReplay,
	}
	processor := &synchronousRememberProcessor{ledger: ledger}

	status, err := processor.failure(context.Background(), input, "loser-submission", synchronousTerminalBase(input, "loser-submission", nil), "embedding", 0, time.Now(), errors.New("embedding failed"))
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, "winner-submission", status.SubmissionID)
	require.Equal(t, "winner-correlation", status.CorrelationID)
	require.Equal(t, "completed", status.ProcessingState)
}

func TestSynchronousFailureRecordUsesBoundedCleanupContext(t *testing.T) {
	input := synchronousPipelineRememberRequest(uuid.NewString(), uuid.NewString(), "bounded-failure", "bounded-failure-hash")
	ledger := &boundedFailureRecordLedger{synchronousPipelineLedger: &synchronousPipelineLedger{}}
	processor := &synchronousRememberProcessor{ledger: ledger}

	_, err := processor.failure(context.Background(), input, uuid.NewString(), synchronousTerminalBase(input, uuid.NewString(), nil), "embedding", 0, time.Now(), errors.New("embedding failed"))

	require.ErrorIs(t, err, remember.ErrRememberPersistence)
	require.True(t, ledger.sawDeadline)
}

func TestSynchronousPreflightReplaysConcurrentTerminalWinner(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	winnerID := uuid.NewString()
	input := remember.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "preflight-replay-key", RequestHash: "preflight-replay-hash",
		SecurityRejected: true, Metadata: map[string]any{"actor": map[string]any{"correlation_id": "winner-correlation"}},
	}
	winner := &remember.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: winnerID, SubmissionKind: "remember",
		ProcessingState: "completed", SearchState: "current", CorrelationID: "winner-correlation",
		Evidence: []remember.TerminalEvidenceResult{}, RelationshipResults: []remember.SubmissionRelationshipResult{},
		Errors: []remember.SubmissionStatusError{}, Kind: remember.ResultKindTerminal,
	}
	public, err := terminalMap(winner)
	require.NoError(t, err)
	ledger := &synchronousPreflightReplayLedger{
		synchronousPipelineLedger: &synchronousPipelineLedger{},
		winner:                    &repository.RememberAttempt{AttemptID: winnerID, RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public},
	}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	status, err := processor.ProcessRemember(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, winnerID, status.SubmissionID)
	require.Equal(t, "winner-correlation", status.CorrelationID)
	require.Equal(t, "completed", status.ProcessingState)
	require.Equal(t, 2, ledger.loadCalls)
}

func TestSynchronousProcessorBuildsTerminalResultFromAssessmentDerivedTypedValue(t *testing.T) {
	ledger := &synchronousPipelineLedger{}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	result, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-typed-value", "pipeline-typed-value-hash"))
	if err != nil {
		var processErr *remember.RememberProcessError
		if errors.As(err, &processErr) && processErr.Err != nil {
			t.Fatalf("process synchronous typed value: %v", processErr.Err)
		}
		t.Fatalf("process synchronous typed value: %v", err)
	}
	if result.ProcessingState != "completed" {
		t.Fatalf("processing state = %q, want completed", result.ProcessingState)
	}
	require.Equal(t, 1, embeddings.calls)
	require.Len(t, embeddings.batches, 1)
	require.Equal(t, []string{"synchronous pipeline document a", "synchronous pipeline document b"}, embeddings.batches[0])
	require.Equal(t, 1, ledger.planCalls)
	require.Equal(t, 1, ledger.commitCalls)
}

func TestSynchronousProcessorTrimsRelationshipRefsInTerminalResult(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-trimmed-ref", "pipeline-trimmed-ref-hash")
	relationships := input.Proposal["relationship_hints"].([]map[string]any)
	relationships[0]["ref"] = "  durable-store  "
	ledger := &synchronousPipelineLedger{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	status, err := processor.ProcessRemember(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, "completed", status.ProcessingState)
	require.Len(t, status.RelationshipResults, 1)
	require.Equal(t, "durable-store", status.RelationshipResults[0].RelationshipRef)
	require.Equal(t, "stored", status.RelationshipResults[0].Disposition)
	require.Len(t, status.RelationshipResults[0].Splits, 1)
}

func TestSynchronousProcessorValidatesIngestBeforeAssessment(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-invalid-ingest", "pipeline-invalid-ingest-hash")
	input.Evidence[0].SupersedesEvidenceIDs = []string{"not-a-uuid"}
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid UUID")
	require.Zero(t, provider.assessCalls)
	require.Zero(t, ledger.recordFailureCalls)
	require.Zero(t, ledger.planCalls)
}

func TestSynchronousProcessorClassifiesAssessmentInputBudget(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{}
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: limits,
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-assessment-budget", "pipeline-assessment-budget-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, string(remember.TerminalErrorInputBudgetExceeded), processErr.Result.Errors[0].Code)
	require.Equal(t, string(remember.TerminalErrorInputBudgetExceeded), ledger.failureInput.Attempt.ErrorCode)
	require.Zero(t, provider.assessCalls)
	require.Zero(t, ledger.planCalls)
}

func TestSynchronousProcessorReplaysMatchingTerminalBeforeProviderWork(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-replay", "pipeline-replay-hash")
	winner := &remember.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
		ProcessingState: "completed", SearchState: "current", CorrelationID: "winner-correlation",
		Evidence: []remember.TerminalEvidenceResult{{
			Disposition: "stored", EvidenceID: uuid.NewString(), EvidenceIndex: 0,
			SupersededEvidenceIDs: []string{}, SearchState: "current",
		}},
		RelationshipResults: []remember.SubmissionRelationshipResult{{
			RelationshipRef: "durable-store", Disposition: "stored", Splits: []remember.SubmissionRelationshipSplit{{
				SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active",
			}},
		}},
		Errors: []remember.SubmissionStatusError{}, Kind: remember.ResultKindTerminal,
	}
	public, err := terminalMap(winner)
	require.NoError(t, err)
	ledger := &synchronousPipelineLedger{loadResult: &repository.RememberAttempt{
		AttemptID: winner.SubmissionID, RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion,
		Outcome: "completed", PublicResult: public,
	}}
	provider := &synchronousPipelineProvider{}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	status, err := processor.ProcessRemember(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, status)
	require.NotNil(t, status.Terminal)
	replayed, err := terminalMap(status.Terminal)
	require.NoError(t, err)
	require.Equal(t, public, replayed)
	require.Equal(t, "winner-correlation", status.CorrelationID)
	require.Equal(t, 1, ledger.loadCalls)
	require.Zero(t, provider.assessCalls)
	require.Zero(t, embeddings.calls)
	require.Zero(t, ledger.planCalls)
	require.Zero(t, ledger.commitCalls)
}

func TestSynchronousAttemptStatusReordersMatchingRelationshipReplay(t *testing.T) {
	input := remember.RememberProcessRequest{Proposal: map[string]any{
		"relationship_hints": []map[string]any{{"ref": "first"}, {"ref": "second"}},
	}}
	terminal := &remember.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
		ProcessingState: "completed", SearchState: "current", CorrelationID: "replay-correlation",
		Evidence: []remember.TerminalEvidenceResult{}, Errors: []remember.SubmissionStatusError{}, Kind: remember.ResultKindTerminal,
		RelationshipResults: []remember.SubmissionRelationshipResult{
			{RelationshipRef: "second", Disposition: "stored", Splits: []remember.SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active"}}},
			{RelationshipRef: "first", Disposition: "stored", Splits: []remember.SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active"}}},
		},
	}
	public, err := terminalMap(terminal)
	require.NoError(t, err)

	status, err := synchronousAttemptStatus(&repository.RememberAttempt{RequestHash: "hash", ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public}, input)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, []string{status.RelationshipResults[0].RelationshipRef, status.RelationshipResults[1].RelationshipRef})
	require.Equal(t, []string{"first", "second"}, []string{status.Terminal.RelationshipResults[0].RelationshipRef, status.Terminal.RelationshipResults[1].RelationshipRef})
}

func TestSynchronousProcessorRejectsChangedTerminalHashBeforeProviderWork(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-conflict", "current-hash")
	ledger := &synchronousPipelineLedger{loadResult: &repository.RememberAttempt{
		RequestHash: "different-hash", ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: map[string]any{},
	}}
	provider := &synchronousPipelineProvider{}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.ErrorIs(t, err, remember.ErrRememberConflict)
	require.Equal(t, 1, ledger.loadCalls)
	require.Zero(t, provider.assessCalls)
	require.Zero(t, embeddings.calls)
	require.Zero(t, ledger.planCalls)
	require.Zero(t, ledger.commitCalls)
}

func TestSynchronousProcessorAuditsSecurityRejectedHashConflict(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-security-conflict", "current-hash")
	input.SecurityRejected = true
	input.SecurityRejectionAudit = &remember.SecurityRejectionAuditInput{EventID: uuid.NewString(), TeamID: teamID, ActorProfileID: ownerID, Surface: "remember", ReasonCode: "evidence_security_rejected", EvidenceCount: 1}
	ledger := &synchronousPipelineLedger{loadResult: &repository.RememberAttempt{
		RequestHash: "different-hash", ContractVersion: domain.ContractVersion, Outcome: "quarantined",
	}}
	audit := &synchronousSecurityAuditStub{}
	provider := &synchronousPipelineProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}), Auditor: audit,
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.ErrorIs(t, err, remember.ErrRememberConflict)
	require.Equal(t, 1, audit.calls)
	require.Zero(t, ledger.preflightCalls)
	require.Zero(t, provider.assessCalls)
}

func TestSynchronousProcessorReturnsBoundedConflictForExistingLegacyIngest(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousExistingLegacyLedger{
		synchronousPipelineLedger: &synchronousPipelineLedger{},
	}
	provider := &synchronousPipelineProvider{}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-existing-legacy", "pipeline-existing-legacy-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.NotNil(t, processErr.Result)
	require.Equal(t, "failed", processErr.Result.ProcessingState)
	require.Len(t, processErr.Result.Errors, 1)
	require.Equal(t, string(remember.TerminalErrorIdempotencyConflict), processErr.Result.Errors[0].Code)
	require.Equal(t, 1, ledger.commitCalls)
	require.Equal(t, 1, ledger.recordFailureCalls)
	require.Equal(t, "commit", ledger.failureInput.Attempt.FailedPhase)
	require.Equal(t, 1, embeddings.calls)
}

func TestSynchronousProcessorSkipsEmbeddingForPreflightQuarantine(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-preflight-quarantine", "pipeline-preflight-quarantine-hash")
	input.SecurityRejected = true
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	status, err := processor.ProcessRemember(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, "quarantined", status.ProcessingState)
	require.Len(t, status.Errors, 1)
	require.Equal(t, string(remember.TerminalErrorQuarantined), status.Errors[0].Code)
	require.Equal(t, 1, ledger.preflightCalls)
	require.Zero(t, provider.assessCalls)
	require.Zero(t, embeddings.calls)
	require.Zero(t, ledger.planCalls)
	require.Zero(t, ledger.commitCalls)
	require.Zero(t, ledger.terminalCalls)
}

func TestSynchronousPreflightAuditRunsOnlyForNewQuarantine(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-security-replay", "pipeline-security-replay-hash")
	input.SecurityRejected = true
	audit := &synchronousSecurityAuditStub{}
	ledger := &synchronousSecurityReplayLedger{synchronousPipelineLedger: &synchronousPipelineLedger{}}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}), Auditor: audit,
	})
	auditInput := remember.SecurityRejectionAuditInput{EventID: uuid.NewString(), TeamID: teamID, ActorProfileID: ownerID, Surface: "remember", ReasonCode: "evidence_security_rejected", EvidenceCount: 1}
	input.SecurityRejectionAudit = &auditInput

	first, err := processor.ProcessRemember(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "quarantined", first.ProcessingState)
	require.Equal(t, 1, audit.calls)
	require.Equal(t, 1, ledger.preflightCalls)

	second, err := processor.ProcessRemember(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, first.SubmissionID, second.SubmissionID)
	require.Equal(t, first.CorrelationID, second.CorrelationID)
	require.Equal(t, 1, audit.calls)
	require.Equal(t, 1, ledger.preflightCalls)

	audit.err = errors.New("audit unavailable")
	third, err := processor.ProcessRemember(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, first.SubmissionID, third.SubmissionID)
	require.Equal(t, 1, audit.calls)
}

func TestSynchronousProcessorSkipsEmbeddingForNoSupportedMemory(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{response: func(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
		time.Sleep(20 * time.Millisecond)
		return synchronousPipelineUnsupportedResponse(request)
	}}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	status, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-no-supported", "pipeline-no-supported-hash"))

	require.NoError(t, err)
	require.Equal(t, "rejected", status.ProcessingState)
	require.Len(t, status.Errors, 1)
	require.Equal(t, string(remember.TerminalErrorNoSupportedMemory), status.Errors[0].Code)
	require.Equal(t, 1, provider.assessCalls)
	require.Zero(t, embeddings.calls)
	require.Zero(t, ledger.planCalls)
	require.Zero(t, ledger.commitCalls)
	require.Equal(t, 1, ledger.terminalCalls)
	require.GreaterOrEqual(t, ledger.terminalInput.Attempt.Duration, 15*time.Millisecond)
}

func TestSynchronousProcessorSecuritySignalsOverrideNoSupportedMemory(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{response: func(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
		response := synchronousPipelineUnsupportedResponse(request)
		evidence := request.Evidence[0]
		startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidence, 0)
		endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidence, len([]rune(evidence.Content)))
		if !startOK || !endOK {
			t.Fatal("synchronous assessment evidence boundaries are required")
		}
		response.SecuritySignals = []assessor.SemanticAssessmentSecuritySignal{{
			EvidenceID: evidence.EvidenceID, Kind: "instruction_override", StartRef: startRef, EndRef: endRef,
		}}
		return response
	}}
	embeddings := &synchronousPipelineEmbeddingProvider{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	status, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-security-wins", "pipeline-security-wins-hash"))

	require.NoError(t, err)
	require.Equal(t, "quarantined", status.ProcessingState)
	require.Len(t, status.Errors, 1)
	require.Equal(t, string(remember.TerminalErrorQuarantined), status.Errors[0].Code)
	require.Equal(t, 1, ledger.terminalCalls)
	require.Equal(t, "quarantined", ledger.terminalInput.Attempt.Outcome)
	require.Zero(t, embeddings.calls)
	require.Zero(t, ledger.planCalls)
}

func TestSynchronousProcessorMapsEmbeddingResponseFailureToTerminal(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousPipelineProvider{}
	embeddings := &synchronousPipelineEmbeddingProvider{embeddings: []semanticwrite.IndexedEmbedding{}}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(embeddings),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-embedding-invalid", "pipeline-embedding-invalid-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.NotNil(t, processErr.Result)
	require.Equal(t, "failed", processErr.Result.ProcessingState)
	require.Len(t, processErr.Result.Errors, 1)
	require.Equal(t, string(remember.TerminalErrorEmbeddingResponseInvalid), processErr.Result.Errors[0].Code)
	require.Equal(t, 1, embeddings.calls)
	require.Equal(t, 1, ledger.recordFailureCalls)
	require.Equal(t, "failed", ledger.failureInput.Attempt.Outcome)
	require.Equal(t, "embedding", ledger.failureInput.Attempt.FailedPhase)
	require.Equal(t, string(remember.TerminalErrorEmbeddingResponseInvalid), ledger.failureInput.Attempt.ErrorCode)
	require.Zero(t, ledger.commitCalls)
	require.Zero(t, ledger.terminalCalls)
}

func synchronousPipelineRememberRequest(teamID, ownerID, key, hash string) remember.RememberProcessRequest {
	return remember.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: hash,
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "synchronous-pipeline-correlation"}},
		Evidence: []remember.EvidenceInput{{Content: "Dense-Mem stores its durable memory in PostgreSQL.", SourceType: "manual", Authority: "primary"}},
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"ref": "durable-store", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "stores_memory_in"},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}}},
	}
}

type synchronousPipelineCatalog struct{}

func (synchronousPipelineCatalog) ListSubmissionAssessmentEntityCatalog(_ context.Context, input repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error) {
	groups := make([]repository.SubmissionAssessmentEntityCatalogGroup, 0, len(input.Entities))
	for _, entity := range input.Entities {
		groups = append(groups, repository.SubmissionAssessmentEntityCatalogGroup{Ref: entity.Ref, Candidates: []repository.SemanticReviewEntityCandidate{}, Complete: true})
	}
	return repository.SubmissionAssessmentEntityCatalogResult{Groups: groups, Complete: true}, nil
}

func (synchronousPipelineCatalog) ResolveSemanticReviewPredicateCandidates(context.Context, repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error) {
	return []repository.SemanticReviewPredicateResolution{}, nil
}

func (synchronousPipelineCatalog) ListSemanticAssessmentPredicateOptions(context.Context, repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error) {
	return []repository.SemanticReviewPredicateCandidate{}, nil
}

type synchronousPipelineSession struct{}

func (synchronousPipelineSession) SessionID() string { return "synchronous-pipeline" }

type synchronousPipelineProvider struct {
	response    func(assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse
	assessCalls int
	repairCalls int
}

type synchronousMalformedProvider struct {
	repairs int
}

func (p *synchronousMalformedProvider) Assess(context.Context, assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	time.Sleep(10 * time.Millisecond)
	return synchronousPipelineSession{}, synchronousMalformedTurn(), nil
}

func (p *synchronousMalformedProvider) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	p.repairs++
	time.Sleep(10 * time.Millisecond)
	return synchronousMalformedTurn(), nil
}

func (*synchronousMalformedProvider) ModelName() string { return "synchronous-malformed" }

func synchronousMalformedTurn() assessor.SemanticAssessmentTurn {
	return assessor.SemanticAssessmentTurn{
		ValidationStage:  "response_contract",
		ValidationErrors: []assessor.SemanticValidationError{{Field: "response", Message: "invalid"}},
	}
}

func (p *synchronousPipelineProvider) Assess(_ context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	p.assessCalls++
	response := synchronousPipelineResponse(request)
	if p.response != nil {
		response = p.response(request)
	}
	return synchronousPipelineSession{}, assessor.SemanticAssessmentTurn{Response: response}, nil
}

func (p *synchronousPipelineProvider) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	p.repairCalls++
	return assessor.SemanticAssessmentTurn{}, errors.New("unexpected synchronous assessment repair")
}

func (*synchronousPipelineProvider) ModelName() string { return "synchronous-pipeline" }

func synchronousPipelineResponse(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
	evidence := request.Evidence[0]
	startRef, startOK := assessor.SemanticAssessmentBoundaryRef(evidence, 0)
	endRef, endOK := assessor.SemanticAssessmentBoundaryRef(evidence, len([]rune(evidence.Content)))
	if !startOK || !endOK {
		panic("synchronous assessment evidence boundaries are required")
	}
	rangeValue := assessor.SemanticAssessmentGroundedRange{EvidenceID: evidence.EvidenceID, StartRef: startRef, EndRef: endRef}
	entities := make([]assessor.SemanticAssessmentEntityResult, 0, len(request.SubmittedEntities))
	for _, entity := range request.SubmittedEntities {
		groundingRef := entity.Groundings[0].GroundingRef
		entities = append(entities, assessor.SemanticAssessmentEntityResult{Ref: entity.Ref, GroundingRef: &groundingRef, Action: "create"})
	}
	relationships := make([]assessor.SemanticAssessmentRelationshipResult, 0, len(request.SubmittedRelationships))
	for _, relationship := range request.SubmittedRelationships {
		value := relationship.ObjectValue
		if value != nil {
			copy := *value
			value = &copy
		}
		relationships = append(relationships, assessor.SemanticAssessmentRelationshipResult{
			Ref: relationship.Ref, Disposition: "stored", Splits: []assessor.SemanticAssessmentRelationshipSplit{{
				SplitIndex: 0, SubjectRef: relationship.SubjectRef, PredicateRange: rangeValue,
				PredicateStatus: "registration_required", PredicateRegistration: &assessor.SemanticAssessmentPredicateRegistration{
					PredicateKey: relationship.PredicateHint, RelationshipKind: "state", CurrentCardinality: "many",
				},
				ObjectRef: relationship.ObjectRef, ObjectValue: value, ValueRange: &rangeValue, Polarity: relationship.Polarity,
				SupportRanges: []assessor.SemanticAssessmentGroundedRange{rangeValue}, ValidFrom: relationship.ValidFrom, ValidTo: relationship.ValidTo,
			}},
		})
	}
	return assessor.SemanticAssessmentResponse{RequestID: request.RequestID, SecuritySignals: []assessor.SemanticAssessmentSecuritySignal{}, EntityResults: entities, RelationshipResults: relationships}
}

func synchronousPipelineUnsupportedResponse(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
	response := synchronousPipelineResponse(request)
	for index := range response.RelationshipResults {
		reason := "not_supported_by_evidence"
		response.RelationshipResults[index].Disposition = "not_supported"
		response.RelationshipResults[index].Reason = &reason
		response.RelationshipResults[index].Splits = []assessor.SemanticAssessmentRelationshipSplit{}
	}
	return response
}

type synchronousPipelineEmbeddingProvider struct {
	calls      int
	batches    [][]string
	embeddings []semanticwrite.IndexedEmbedding
}

func (p *synchronousPipelineEmbeddingProvider) EmbedBatch(_ context.Context, texts []string) ([]semanticwrite.IndexedEmbedding, string, error) {
	p.calls++
	p.batches = append(p.batches, append([]string(nil), texts...))
	if p.embeddings != nil {
		return append([]semanticwrite.IndexedEmbedding(nil), p.embeddings...), p.ModelName(), nil
	}
	result := make([]semanticwrite.IndexedEmbedding, len(texts))
	for index := range texts {
		result[index] = semanticwrite.IndexedEmbedding{Index: index, Vector: []float32{1}}
	}
	return result, p.ModelName(), nil
}

func (*synchronousPipelineEmbeddingProvider) ModelName() string {
	return "synchronous-pipeline-embedding"
}
func (*synchronousPipelineEmbeddingProvider) Dimensions() int   { return 1 }
func (*synchronousPipelineEmbeddingProvider) IsAvailable() bool { return true }

type synchronousPipelineLedger struct {
	loadResult         *repository.RememberAttempt
	loadErr            error
	recordFailureErr   error
	loadCalls          int
	planCalls          int
	commitCalls        int
	terminalCalls      int
	preflightCalls     int
	recordFailureCalls int
	failureInput       repository.RememberFailureRecordInput
	terminalInput      repository.SynchronousRememberTerminalInput
}

type synchronousExistingLegacyLedger struct {
	*synchronousPipelineLedger
}

func (ledger *synchronousExistingLegacyLedger) CommitSynchronousRemember(context.Context, repository.SynchronousRememberCommitInput) (*repository.SynchronousRememberCommitResult, error) {
	ledger.commitCalls++
	return nil, fmt.Errorf("%w: existing ingest has no synchronous terminal attempt", repository.ErrIdempotencyConflict)
}

type boundedFailureRecordLedger struct {
	*synchronousPipelineLedger
	sawDeadline bool
}

func (ledger *boundedFailureRecordLedger) RecordRememberFailure(ctx context.Context, input repository.RememberFailureRecordInput) error {
	_, ledger.sawDeadline = ctx.Deadline()
	return errors.New("failure record timed out")
}

type synchronousSecurityReplayLedger struct {
	*synchronousPipelineLedger
	winner *repository.RememberAttempt
}

func (ledger *synchronousSecurityReplayLedger) LoadRememberAttempt(_ context.Context, _ repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	ledger.loadCalls++
	if ledger.winner == nil {
		return nil, repository.ErrRememberAttemptNotFound
	}
	return ledger.winner, nil
}

func (ledger *synchronousSecurityReplayLedger) RecordSynchronousRememberPreflightQuarantine(_ context.Context, input repository.RememberAttemptRecordInput) error {
	ledger.preflightCalls++
	public := make(map[string]any, len(input.PublicResult))
	for key, value := range input.PublicResult {
		public[key] = value
	}
	ledger.winner = &repository.RememberAttempt{AttemptID: input.AttemptID, RequestHash: input.RequestHash, ContractVersion: input.ContractVersion, Outcome: input.Outcome, PublicResult: public}
	return nil
}

type synchronousSecurityAuditStub struct {
	calls int
	err   error
}

func (stub *synchronousSecurityAuditStub) RecordSecurityRejection(context.Context, remember.SecurityRejectionAuditInput) error {
	stub.calls++
	return stub.err
}

type synchronousPreflightReplayLedger struct {
	*synchronousPipelineLedger
	winner    *repository.RememberAttempt
	loadCalls int
}

func (ledger *synchronousPreflightReplayLedger) LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	ledger.loadCalls++
	if ledger.loadCalls == 1 {
		return nil, repository.ErrRememberAttemptNotFound
	}
	return ledger.winner, nil
}

func (*synchronousPreflightReplayLedger) RecordSynchronousRememberPreflightQuarantine(context.Context, repository.RememberAttemptRecordInput) error {
	return repository.ErrRememberReplay
}

func (ledger *synchronousPipelineLedger) LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	ledger.loadCalls++
	if ledger.loadErr != nil {
		return nil, ledger.loadErr
	}
	if ledger.loadResult != nil {
		return ledger.loadResult, nil
	}
	return nil, repository.ErrRememberAttemptNotFound
}

func (ledger *synchronousPipelineLedger) PlanSynchronousRememberEmbeddings(_ context.Context, _ repository.CreateIngestInput, _ repository.CommitSubmissionAssessmentInput) (*repository.SynchronousRememberEmbeddingPlan, error) {
	ledger.planCalls++
	return &repository.SynchronousRememberEmbeddingPlan{
		EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 1, EmbeddingModel: "synchronous-pipeline-embedding",
		SearchGenerationID: uuid.NewString(), SearchGenerationVersion: 1,
		Documents: []repository.SynchronousRememberEmbeddingDocument{
			{Hash: "synchronous-pipeline-document-a", Text: "synchronous pipeline document a"},
			{Hash: "synchronous-pipeline-document-b", Text: "synchronous pipeline document b"},
		},
	}, nil
}

func (ledger *synchronousPipelineLedger) CommitSynchronousRemember(_ context.Context, input repository.SynchronousRememberCommitInput) (*repository.SynchronousRememberCommitResult, error) {
	ledger.commitCalls++
	created := synchronousPipelineCreated(input.CreateIngest)
	scope := repository.SubmissionAssessmentRunScope{
		TeamID: created.TeamID, OwnerProfileID: created.OwnerProfileID, IngestID: created.IngestID, PlacementRunID: created.PlacementRunID,
		WorkerID: "synchronous-pipeline", ExpectedAttempts: 1, MaxAttempts: 1,
	}
	_, commit, err := input.BuildCommit(created, scope)
	if err != nil {
		return nil, err
	}
	committed := &repository.CommitSubmissionAssessmentResult{}
	for _, observation := range commit.RelationshipObservations {
		committed.RelationshipResults = append(committed.RelationshipResults, repository.RelationshipDecisionResult{
			ProposalID:   observation.Observation.Ref,
			Relationship: &repository.RelationshipRecord{RelationshipID: uuid.NewString(), Version: 1, Status: "active"},
		})
	}
	publicResult, err := input.BuildPublicResult(created, committed)
	if err != nil {
		return nil, err
	}
	input.Attempt.PublicResult = publicResult
	return &repository.SynchronousRememberCommitResult{Ingest: created, Commit: committed, Attempt: &repository.RememberAttempt{
		AttemptID: input.Attempt.AttemptID, RequestHash: input.Attempt.RequestHash, ContractVersion: input.Attempt.ContractVersion,
		Outcome: input.Attempt.Outcome, PublicResult: input.Attempt.PublicResult,
	}}, nil
}

func (ledger *synchronousPipelineLedger) CommitSynchronousRememberTerminal(_ context.Context, input repository.SynchronousRememberTerminalInput) (*repository.SynchronousRememberCommitResult, error) {
	ledger.terminalCalls++
	ledger.terminalInput = input
	created := synchronousPipelineCreated(input.CreateIngest)
	scope := synchronousPipelineRunScope(created)
	if _, _, err := input.BuildTerminal(created, scope); err != nil {
		return nil, err
	}
	committed := &repository.CommitSubmissionAssessmentResult{}
	publicResult, err := input.BuildPublicResult(created, committed)
	if err != nil {
		return nil, err
	}
	input.Attempt.PublicResult = publicResult
	return &repository.SynchronousRememberCommitResult{Ingest: created, Commit: committed, Attempt: &repository.RememberAttempt{
		AttemptID: input.Attempt.AttemptID, RequestHash: input.Attempt.RequestHash, ContractVersion: input.Attempt.ContractVersion,
		Outcome: input.Attempt.Outcome, PublicResult: input.Attempt.PublicResult,
	}}, nil
}

func (ledger *synchronousPipelineLedger) RecordSynchronousRememberPreflightQuarantine(context.Context, repository.RememberAttemptRecordInput) error {
	ledger.preflightCalls++
	return nil
}

func (ledger *synchronousPipelineLedger) RecordRememberFailure(_ context.Context, input repository.RememberFailureRecordInput) error {
	ledger.recordFailureCalls++
	ledger.failureInput = input
	return ledger.recordFailureErr
}

func synchronousPipelineRunScope(created *repository.CreateIngestResult) repository.SubmissionAssessmentRunScope {
	return repository.SubmissionAssessmentRunScope{
		TeamID: created.TeamID, OwnerProfileID: created.OwnerProfileID, IngestID: created.IngestID, PlacementRunID: created.PlacementRunID,
		WorkerID: "synchronous-pipeline", ExpectedAttempts: 1, MaxAttempts: 1,
	}
}

func synchronousPipelineCreated(input repository.CreateIngestInput) *repository.CreateIngestResult {
	created := &repository.CreateIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: uuid.NewString(), PlacementRunID: uuid.NewString(), Proposal: input.Proposal}
	for index, evidence := range input.Evidence {
		fragmentID := uuid.NewString()
		created.Evidence = append(created.Evidence, repository.EvidenceFragment{FragmentID: fragmentID, EvidenceIndex: index, Content: evidence.Content, ContentHash: evidence.ContentHash, Authority: evidence.Authority, SupersededEvidenceIDs: []string{}})
		created.Items = append(created.Items, repository.PlacementItem{PlacementItemID: uuid.NewString(), FragmentID: fragmentID, EvidenceIndex: index, Status: "queued"})
	}
	return created
}
