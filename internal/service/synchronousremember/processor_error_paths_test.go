package synchronousremember

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousProcessorRequiresDependencies(t *testing.T) {
	var processor *synchronousRememberProcessor

	_, err := processor.ProcessRemember(context.Background(), remember.RememberProcessRequest{})

	require.ErrorContains(t, err, "synchronous processor dependencies are required")
}

func TestSynchronousProcessorReturnsPersistenceWhenSecurityConflictAuditFails(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-security-conflict-audit-failure", "current-hash")
	input.SecurityRejected = true
	input.SecurityRejectionAudit = &remember.SecurityRejectionAuditInput{EventID: uuid.NewString(), TeamID: teamID, ActorProfileID: ownerID, Surface: "remember", ReasonCode: "evidence_security_rejected", EvidenceCount: 1}
	ledger := &synchronousPipelineLedger{loadResult: &repository.RememberAttempt{RequestHash: "different-hash", ContractVersion: domain.ContractVersion, Outcome: "quarantined"}}
	audit := &synchronousSecurityAuditStub{err: errors.New("audit unavailable")}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}), Auditor: audit,
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.ErrorIs(t, err, remember.ErrRememberPersistence)
	require.Equal(t, 1, audit.calls)
	require.Zero(t, ledger.preflightCalls)
}

func TestSynchronousProcessorReturnsPersistenceWhenPreflightAuditFails(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-preflight-audit-failure", "pipeline-preflight-audit-failure-hash")
	input.SecurityRejected = true
	input.SecurityRejectionAudit = &remember.SecurityRejectionAuditInput{EventID: uuid.NewString(), TeamID: teamID, ActorProfileID: ownerID, Surface: "remember", ReasonCode: "evidence_security_rejected", EvidenceCount: 1}
	ledger := &synchronousPipelineLedger{}
	audit := &synchronousSecurityAuditStub{err: errors.New("audit unavailable")}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}), Auditor: audit,
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.ErrorIs(t, err, remember.ErrRememberPersistence)
	require.Equal(t, 1, audit.calls)
	require.Zero(t, ledger.preflightCalls)
}

func TestSynchronousProcessorReturnsReplayLoadFailureAfterPreflightRace(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-preflight-replay-load-failure", "pipeline-preflight-replay-load-failure-hash")
	input.SecurityRejected = true
	ledger := &synchronousPreflightReplayLoadErrorLedger{synchronousPipelineLedger: &synchronousPipelineLedger{}}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	require.ErrorContains(t, err, "winner lookup failed")
	require.Equal(t, 1, ledger.preflightCalls)
	require.Equal(t, 2, ledger.loadCalls)
}

func TestSynchronousProcessorRecordsPreflightFailureAsTerminal(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	input := synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-preflight-record-failure", "pipeline-preflight-record-failure-hash")
	input.SecurityRejected = true
	ledger := &synchronousPreflightRecordErrorLedger{
		synchronousPipelineLedger: &synchronousPipelineLedger{},
		err:                       errors.New("preflight record failed"),
	}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), input)

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, string(remember.TerminalErrorCommitConflict), processErr.Result.Errors[0].Code)
	require.Equal(t, 1, ledger.preflightCalls)
	require.Equal(t, 1, ledger.recordFailureCalls)
}

func TestSynchronousProcessorRecordsInitialLedgerLoadFailure(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{loadErr: errors.New("attempt lookup failed")}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "initial-load-failure", "initial-load-failure-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, "commit", ledger.failureInput.Attempt.FailedPhase)
	require.Equal(t, string(remember.TerminalErrorCommitConflict), ledger.failureInput.Attempt.ErrorCode)
	require.Equal(t, 1, ledger.recordFailureCalls)
}

func TestSynchronousAttemptStatusRejectsMalformedReplayShapes(t *testing.T) {
	_, err := synchronousAttemptStatus(nil, remember.RememberProcessRequest{})
	require.ErrorContains(t, err, "replay attempt is required")

	_, err = synchronousAttemptStatus(&repository.RememberAttempt{PublicResult: map[string]any{"unsupported": make(chan int)}}, remember.RememberProcessRequest{})
	require.Error(t, err)

	_, err = synchronousAttemptStatus(&repository.RememberAttempt{PublicResult: map[string]any{"processing_state": []any{"invalid"}}}, remember.RememberProcessRequest{})
	require.Error(t, err)

	_, err = synchronousAttemptStatus(&repository.RememberAttempt{PublicResult: map[string]any{
		"evidence": []map[string]any{{"disposition": 7}},
	}}, remember.RememberProcessRequest{})
	require.Error(t, err)
}

func TestSynchronousRelationshipRefsDefaultBlankReferences(t *testing.T) {
	refs := synchronousRelationshipRefs(map[string]any{
		"relationship_hints": []map[string]any{{}, {"ref": "  explicit  "}},
	})

	require.Equal(t, []string{"relationship:0", "explicit"}, refs)
}

func TestReorderSynchronousRelationshipResultsRejectsAmbiguousShapes(t *testing.T) {
	cases := []struct {
		name    string
		results []remember.SubmissionRelationshipResult
		refs    []string
	}{
		{name: "length mismatch", results: []remember.SubmissionRelationshipResult{{RelationshipRef: "first"}}, refs: []string{"first", "second"}},
		{name: "blank result", results: []remember.SubmissionRelationshipResult{{}}, refs: []string{"first"}},
		{name: "duplicate result", results: []remember.SubmissionRelationshipResult{{RelationshipRef: "first"}, {RelationshipRef: "first"}}, refs: []string{"first", "second"}},
		{name: "blank requested ref", results: []remember.SubmissionRelationshipResult{{RelationshipRef: "first"}}, refs: []string{""}},
		{name: "duplicate requested ref", results: []remember.SubmissionRelationshipResult{{RelationshipRef: "first"}, {RelationshipRef: "second"}}, refs: []string{"first", "first"}},
		{name: "missing requested ref", results: []remember.SubmissionRelationshipResult{{RelationshipRef: "first"}}, refs: []string{"second"}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.results, reorderSynchronousRelationshipResults(test.results, test.refs))
		})
	}
}

func TestSynchronousCompletedResultHandlesMissingAndUnmatchedRelationships(t *testing.T) {
	input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "evidence"}}}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0}}}
	committed := &repository.CommitSubmissionAssessmentResult{RelationshipResults: []repository.RelationshipDecisionResult{
		{ProposalID: "unknown-proposal", Relationship: &repository.RelationshipRecord{RelationshipID: uuid.NewString(), Version: 1}},
		{ProposalID: "nil-relationship"},
		{ProposalID: "known-proposal", Relationship: &repository.RelationshipRecord{RelationshipID: uuid.NewString(), Version: 1}},
	}}
	observations := []repository.SubmissionAssessmentRelationshipObservationInput{
		{RelationshipRef: "missing-ref", Observation: repository.PlacementRelationshipDecisionInput{Ref: "unknown-proposal"}, SplitIndex: 1},
		{RelationshipRef: "unknown-ref", Observation: repository.PlacementRelationshipDecisionInput{Ref: "known-proposal"}, SplitIndex: 2},
	}

	result := synchronousCompletedResult(input, created, committed, nil, observations, []string{"known-ref"})

	require.Len(t, result.RelationshipResults, 1)
	require.Equal(t, "not_stored", result.RelationshipResults[0].Disposition)
	require.Equal(t, "not_supported_by_evidence", result.RelationshipResults[0].Reason)
}

func TestSynchronousCompletedResultSortsCommittedSplits(t *testing.T) {
	input := remember.RememberProcessRequest{Evidence: []remember.EvidenceInput{{Content: "evidence"}}}
	created := &repository.CreateIngestResult{IngestID: uuid.NewString(), Evidence: []repository.EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0}}}
	committed := &repository.CommitSubmissionAssessmentResult{RelationshipResults: []repository.RelationshipDecisionResult{
		{ProposalID: "known-proposal-second", Relationship: &repository.RelationshipRecord{RelationshipID: "second", Version: 1, Status: "active"}},
		{ProposalID: "known-proposal-first", Relationship: &repository.RelationshipRecord{RelationshipID: "first", Version: 1, Status: "active"}},
	}}
	observations := []repository.SubmissionAssessmentRelationshipObservationInput{
		{RelationshipRef: "known-ref", Observation: repository.PlacementRelationshipDecisionInput{Ref: "known-proposal-second"}, SplitIndex: 2},
		{RelationshipRef: "known-ref", Observation: repository.PlacementRelationshipDecisionInput{Ref: "known-proposal-first"}, SplitIndex: 1},
	}

	result := synchronousCompletedResult(input, created, committed, []repository.SubmissionRelationshipResultInput{{RelationshipRef: "known-ref", Disposition: "stored"}}, observations, []string{"known-ref"})

	require.Equal(t, []int{1, 2}, []int{result.RelationshipResults[0].Splits[0].SplitIndex, result.RelationshipResults[0].Splits[1].SplitIndex})
}

type synchronousPreflightReplayLoadErrorLedger struct {
	*synchronousPipelineLedger
	loadCalls      int
	preflightCalls int
}

func (ledger *synchronousPreflightReplayLoadErrorLedger) LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	ledger.loadCalls++
	if ledger.loadCalls == 1 {
		return nil, repository.ErrRememberAttemptNotFound
	}
	return nil, errors.New("winner lookup failed")
}

func (ledger *synchronousPreflightReplayLoadErrorLedger) RecordSynchronousRememberPreflightQuarantine(context.Context, repository.RememberAttemptRecordInput) error {
	ledger.preflightCalls++
	return repository.ErrRememberReplay
}

type synchronousPreflightRecordErrorLedger struct {
	*synchronousPipelineLedger
	err error
}

func (ledger *synchronousPreflightRecordErrorLedger) RecordSynchronousRememberPreflightQuarantine(context.Context, repository.RememberAttemptRecordInput) error {
	ledger.preflightCalls++
	return ledger.err
}
