package processor

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestInlineEmbeddingResultsFromDocumentsCopiesPlanMetadataAndVectors(t *testing.T) {
	plan := &knowledgecontract.InlineEmbeddingPlan{
		EmbeddingContractID: "contract-v1", EmbeddingDimensions: 2, EmbeddingModel: "embed-v1",
		SearchIndexGenerationID: "search-generation", IndexGeneration: 4,
	}
	documents := []knowledgecontract.SearchDocumentEmbedding{{DocumentHash: "hash-a", Embedding: []float32{1, 2}}}
	results := inlineEmbeddingResultsFromDocuments(documents, plan)
	require.Equal(t, []knowledgecontract.InlineEmbeddingResult{{
		DocumentHash: "hash-a", Embedding: []float32{1, 2}, EmbeddingContractID: "contract-v1",
		EmbeddingDimensions: 2, EmbeddingModel: "embed-v1", SearchIndexGenerationID: "search-generation", IndexGeneration: 4,
	}}, results)
	documents[0].Embedding[0] = 99
	require.Equal(t, float32(1), results[0].Embedding[0])
	require.Empty(t, inlineEmbeddingResultsFromDocuments(nil, plan))
	require.Empty(t, inlineEmbeddingResultsFromDocuments(documents, nil))
}

func TestInlineEmbeddingResultsFromDuplicateDocumentsCopiesPlanMetadata(t *testing.T) {
	plan := &knowledgecontract.RememberDuplicateEmbeddingPlan{
		EmbeddingContractID: "contract-v1", EmbeddingDimensions: 3, EmbeddingModel: "embed-v2",
		SearchIndexGenerationID: "search-generation", IndexGeneration: 7,
	}
	documents := []knowledgecontract.SearchDocumentEmbedding{{DocumentHash: "hash-a", Embedding: []float32{1, 2, 3}}}
	results := inlineEmbeddingResultsFromDuplicateDocuments(documents, plan)
	require.Equal(t, "contract-v1", results[0].EmbeddingContractID)
	require.Equal(t, 3, results[0].EmbeddingDimensions)
	require.Equal(t, "embed-v2", results[0].EmbeddingModel)
	require.Equal(t, "search-generation", results[0].SearchIndexGenerationID)
	require.Equal(t, 7, results[0].IndexGeneration)
	documents[0].Embedding[0] = 99
	require.Equal(t, float32(1), results[0].Embedding[0])
	require.Empty(t, inlineEmbeddingResultsFromDuplicateDocuments(nil, plan))
	require.Empty(t, inlineEmbeddingResultsFromDuplicateDocuments(documents, nil))
}

func TestRememberEmbeddingFailureTypesExposeSentinelsAndCauses(t *testing.T) {
	planCause := errors.New("plan failed")
	var nilPlanFailure *rememberEmbeddingPlanFailure
	require.Equal(t, "remember embedding plan failed", nilPlanFailure.Error())
	require.Nil(t, nilPlanFailure.Unwrap())
	planFailure := &rememberEmbeddingPlanFailure{cause: planCause}
	require.ErrorIs(t, planFailure, planCause)

	configurationFailure := &rememberEmbeddingConfigurationFailure{}
	require.Equal(t, rememberapp.ErrRememberEmbeddingUnavailable.Error(), configurationFailure.Error())
	require.ErrorIs(t, configurationFailure, rememberapp.ErrRememberEmbeddingUnavailable)

	var nilProviderFailure *rememberEmbeddingProviderFailure
	require.Equal(t, rememberapp.ErrRememberEmbeddingUnavailable.Error(), nilProviderFailure.Error())
	require.Equal(t, []error{rememberapp.ErrRememberEmbeddingUnavailable}, nilProviderFailure.Unwrap())
	noCause := &rememberEmbeddingProviderFailure{}
	require.Equal(t, rememberapp.ErrRememberEmbeddingUnavailable.Error(), noCause.Error())
	require.Equal(t, []error{rememberapp.ErrRememberEmbeddingUnavailable}, noCause.Unwrap())
	providerCause := errors.New("provider failed")
	providerFailure := &rememberEmbeddingProviderFailure{cause: providerCause}
	require.Contains(t, providerFailure.Error(), providerCause.Error())
	require.ErrorIs(t, providerFailure, rememberapp.ErrRememberEmbeddingUnavailable)
	require.ErrorIs(t, providerFailure, providerCause)
}

func TestRememberFailureRecoveryErrorsAndCodes(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
		code string
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: "remember failure record persistence timed out: context deadline exceeded", code: "deadline_exceeded"},
		{name: "cancelled", err: context.Canceled, want: "remember failure record persistence was cancelled: context canceled", code: "request_cancelled"},
		{name: "other", err: errors.New("database down"), want: "remember failure record persistence failed", code: "persistence_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(t, rememberFailureRecoveryLogError(test.err), test.want)
			require.Equal(t, test.code, rememberFailureRecoveryErrorCode(test.err))
		})
	}
	var nilLoggerProcessor *rememberSynchronousProcessor
	input := rememberapp.RememberProcessRequest{TeamID: "team", OwnerProfileID: "owner"}
	nilLoggerProcessor.logRememberFailureRecordError(input, "attempt", "assessment", "provider_unavailable", "corr", errors.New("x"))
	nilLoggerProcessor.logRememberFailureRetentionDegraded(input, "attempt", "assessment")
	nilLoggerProcessor.logRememberIdempotencyLockCleanupFailure(input, "attempt")
}

func TestRememberFailureRecoveryLoggingCapturesFailureKinds(t *testing.T) {
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{logger: logger}
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", Metadata: map[string]any{"actor": map[string]any{"correlation_id": "corr"}},
	}
	processor.logRememberFailureRecordError(input, "attempt", "assessment", "provider_unavailable", "corr", context.DeadlineExceeded)
	processor.logRememberFailureRecordError(input, "attempt", "assessment", "provider_unavailable", "corr", context.Canceled)
	processor.logRememberFailureRecordError(input, "attempt", "assessment", "provider_unavailable", "corr", errors.New("x"))
	processor.logRememberFailureRetentionDegraded(input, "attempt", "assessment")
	processor.logRememberIdempotencyLockCleanupFailure(input, "attempt")
	require.Equal(t, []string{
		"remember_failure_record_failed", "remember_failure_record_failed", "remember_failure_record_failed",
	}, logger.errors)
	require.Equal(t, []string{"remember_failure_retention_degraded", "remember_idempotency_lock_cleanup_failed"}, logger.warns)
	require.Equal(t, "corr", rememberProcessCorrelationID(input.Metadata))
	require.Empty(t, rememberProcessCorrelationID(map[string]any{"actor": "wrong"}))
	require.Empty(t, rememberProcessCorrelationID(nil))
}

func TestNewSynchronousProcessorUsesDefaultClassifiersAndCommitStage(t *testing.T) {
	processor := NewSynchronousProcessor(ProcessorDependencies{})
	require.NotNil(t, processor)
	require.NotNil(t, processor.isStaleInput)
	require.True(t, processor.isRememberStaleInput(repository.ErrSourceRevisionConflict))
	require.False(t, processor.isRememberStaleInput(errors.New("ordinary error")))
	require.Empty(t, processor.commitStage(errors.New("commit failed")))
	processor.commitFailureStage = func(error) string { return "write" }
	require.Equal(t, "write", processor.commitStage(errors.New("commit failed")))
	var nilProcessor *rememberSynchronousProcessor
	require.False(t, nilProcessor.isRememberStaleInput(errors.New("ordinary error")))
	require.Empty(t, nilProcessor.commitStage(errors.New("commit failed")))
}

func TestRememberProcessorRejectsMissingLedger(t *testing.T) {
	var nilProcessor *rememberSynchronousProcessor
	_, err := nilProcessor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{})
	require.EqualError(t, err, "remember processor: ledger is required")
	_, err = (&rememberSynchronousProcessor{}).ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{})
	require.EqualError(t, err, "remember processor: ledger is required")
}

func TestRememberProcessorOwnerReturnsProcessingFailureFromLockCallback(t *testing.T) {
	base := &rememberFailureLedgerStub{}
	locker := &rememberWaitAwareLedgerStub{rememberFailureLedgerStub: base}
	processor := &rememberSynchronousProcessor{ledger: locker}
	_, err := processor.ProcessRemember(context.Background(), rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash", SecurityRejected: true,
	})
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPolicyRejected)
	require.Equal(t, 1, locker.lockCalls)
}

func TestRememberProcessorCoversPipelineFailurePhases(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "fact"}},
	}
	t.Run("duplicate plan", func(t *testing.T) {
		ledger := &rememberPipelineLedgerStub{
			rememberFailureLedgerStub: &rememberFailureLedgerStub{},
			duplicatePlanErr:          errors.New("duplicate plan unavailable"),
		}
		_, err := (&rememberSynchronousProcessor{ledger: ledger}).ProcessRemember(context.Background(), input)
		var processErr *rememberapp.RememberProcessError
		require.ErrorAs(t, err, &processErr)
		require.Equal(t, "embedding", ledger.failure.Attempt.FailedPhase)
	})
	t.Run("duplicate resolution", func(t *testing.T) {
		ledger := &rememberPipelineLedgerStub{
			rememberFailureLedgerStub: &rememberFailureLedgerStub{},
			resolveErr:                errors.New("duplicate resolution unavailable"),
		}
		_, err := (&rememberSynchronousProcessor{ledger: ledger}).ProcessRemember(context.Background(), input)
		var processErr *rememberapp.RememberProcessError
		require.ErrorAs(t, err, &processErr)
		require.Equal(t, "embedding", ledger.failure.Attempt.FailedPhase)
	})
	t.Run("duplicate embedding provider unavailable", func(t *testing.T) {
		ledger := &rememberPipelineLedgerStub{
			rememberFailureLedgerStub: &rememberFailureLedgerStub{},
			duplicatePlan: &repository.RememberDuplicateEmbeddingPlan{
				EmbeddingModel: "model", Documents: []repository.SearchDocumentForEmbedding{processorEmbeddingDocument(2)},
			},
		}
		_, err := (&rememberSynchronousProcessor{ledger: ledger}).ProcessRemember(context.Background(), input)
		var processErr *rememberapp.RememberProcessError
		require.ErrorAs(t, err, &processErr)
		require.Equal(t, "embedding", ledger.failure.Attempt.FailedPhase)
		require.Equal(t, string(rememberapp.SubmissionErrorConfigurationInvalid), processErr.Status.Errors[0].Code)
	})

	var nilProcessor *rememberSynchronousProcessor
	_, err := nilProcessor.processRememberUnlocked(context.Background(), input)
	require.EqualError(t, err, "remember processor: ledger is required")
}

func TestRememberProcessorCommitsValidatedAssessmentAndResult(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "fact", ForceInsert: true}},
	}
	commitResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": "committed", "submission_kind": "remember",
		"processing_state": "completed", "search_state": "current", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
	}
	ledger := &rememberPipelineLedgerStub{
		rememberFailureLedgerStub: &rememberFailureLedgerStub{},
		plan:                      &repository.InlineEmbeddingPlan{},
		commitResult:              &repository.SynchronousRememberCommitResult{IngestID: "committed", Outcome: "completed", PublicResult: commitResult},
	}
	processor := &rememberSynchronousProcessor{
		ledger:   ledger,
		catalog:  &processorAssessmentCatalogStub{},
		provider: &processorAssessmentProviderStub{},
	}
	status, err := processor.ProcessRemember(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "committed", status.SubmissionID)
	require.Equal(t, "completed", status.ProcessingState)
}

func TestRememberProcessorExistingAttemptsTakeTerminalAndConflictPaths(t *testing.T) {
	input := rememberapp.RememberProcessRequest{TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash"}
	baseResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": "attempt", "submission_kind": "remember",
		"processing_state": "failed", "search_state": "not_required", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
	}
	tests := []struct {
		name    string
		attempt *repository.RememberAttempt
		wantErr error
	}{
		{name: "unsupported contract", attempt: &repository.RememberAttempt{RequestHash: "hash", ContractVersion: "legacy", Outcome: "completed", PublicResult: baseResult}, wantErr: rememberapp.ErrRememberConflict},
		{name: "nonretryable failure", attempt: &repository.RememberAttempt{AttemptID: "attempt", RequestHash: "hash", ContractVersion: domain.ContractVersion, Outcome: "failed", PublicResult: baseResult}, wantErr: rememberapp.ErrRememberPersistence},
		{name: "in progress", attempt: &repository.RememberAttempt{RequestHash: "hash", ContractVersion: domain.ContractVersion, Outcome: "processing", PublicResult: baseResult}, wantErr: rememberapp.ErrRememberConflict},
		{name: "invalid public result", attempt: &repository.RememberAttempt{AttemptID: "attempt", RequestHash: "hash", ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: map[string]any{"evidence": "invalid"}}, wantErr: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &rememberFailureLedgerStub{load: test.attempt}
			_, err := (&rememberSynchronousProcessor{ledger: ledger}).ProcessRemember(context.Background(), input)
			if test.wantErr == nil {
				require.Error(t, err)
				return
			}
			require.ErrorIs(t, err, test.wantErr)
		})
	}
	ledger := &rememberFailureLedgerStub{loadErr: errors.New("lookup unavailable")}
	_, err := (&rememberSynchronousProcessor{ledger: ledger}).ProcessRemember(context.Background(), input)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, "commit", ledger.failure.Attempt.FailedPhase)
}

func TestRememberAssessmentSecurityRejectedChecksWholeResponse(t *testing.T) {
	require.False(t, rememberAssessmentSecurityRejected(nil))
	require.False(t, rememberAssessmentSecurityRejected(&rememberapp.SynchronousAssessmentResult{}))
	require.False(t, rememberAssessmentSecurityRejected(&rememberapp.SynchronousAssessmentResult{
		Response: assessor.SemanticAssessmentResponse{EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{{Decision: " pass "}}},
	}))
	require.True(t, rememberAssessmentSecurityRejected(&rememberapp.SynchronousAssessmentResult{
		Response: assessor.SemanticAssessmentResponse{EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{{Decision: " REJECT "}}},
	}))
}

func TestNormalizeRememberCommitFailureMapsSearchFenceErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "stale version", err: repository.ErrSearchStaleVersion, want: rememberapp.ErrRememberCommitConflict},
		{name: "contract mismatch", err: repository.ErrSearchContractMismatch, want: rememberapp.ErrRememberCommitConflict},
		{name: "other", err: errors.New("database down"), want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeRememberCommitFailure(test.err)
			if test.want == nil {
				require.ErrorIs(t, got, test.err)
				return
			}
			require.ErrorIs(t, got, test.want)
			require.NotErrorIs(t, got, test.err)
		})
	}
}

func TestRememberFailureAndCommitMetadataCoverSupportedCauses(t *testing.T) {
	planCauses := []error{
		repository.ErrSearchContractMismatch, repository.ErrInlineEmbeddingPlanMismatch,
		repository.ErrInlineEmbeddingPlanTooLarge, context.DeadlineExceeded, context.Canceled, errors.New("other"),
	}
	for _, cause := range planCauses {
		class, code := rememberEmbeddingPlanFailureMetadata(cause)
		require.NotEmpty(t, class)
		require.NotEmpty(t, code)
	}
	commitCauses := []error{
		rememberapp.ErrRememberCommitConflict, repository.ErrInlineEmbeddingPlanMismatch,
		repository.ErrSearchContractMismatch, repository.ErrSearchStaleVersion, rememberapp.ErrRememberStaleInput,
		repository.ErrRememberDuplicateCandidateStale, repository.ErrSourceRevisionConflict,
		repository.ErrEvidenceConflictStaleInput, context.DeadlineExceeded, context.Canceled, errors.New("other"),
	}
	for _, cause := range commitCauses {
		class, code := rememberCommitFailureMetadata(cause)
		require.NotEmpty(t, class)
		require.NotEmpty(t, code)
	}
}

func TestRememberCommitOperationalLogErrorIncludesStageAndContext(t *testing.T) {
	stage := func(error) string { return "persist" }
	require.EqualError(t, rememberCommitOperationalLogError(context.DeadlineExceeded, stage), "remember semantic commit at persist timed out: context deadline exceeded")
	require.EqualError(t, rememberCommitOperationalLogError(context.Canceled, stage), "remember semantic commit at persist was cancelled: context canceled")
	require.EqualError(t, rememberCommitOperationalLogError(errors.New("database down"), stage), "remember semantic commit at persist failed")
	require.EqualError(t, rememberCommitOperationalLogError(errors.New("database down"), nil), "remember semantic commit failed")
}

func TestRememberProcessingFailureLoggingClassifiesFailureSources(t *testing.T) {
	logger := &rememberProcessorLogCapture{}
	processor := &rememberSynchronousProcessor{
		logger:             logger,
		commitFailureStage: func(error) string { return "persist" },
	}
	input := rememberapp.RememberProcessRequest{TeamID: "team", OwnerProfileID: "owner"}
	processor.logRememberFailure(input, "attempt", time.Now(), "embedding", "embedding_unavailable", "corr", &rememberEmbeddingPlanFailure{cause: repository.ErrInlineEmbeddingPlanTooLarge})
	processor.logRememberFailure(input, "attempt", time.Now(), "embedding", "configuration_invalid", "corr", &rememberEmbeddingConfigurationFailure{})
	processor.logRememberFailure(input, "attempt", time.Now(), "embedding", "embedding_response_invalid", "corr", &rememberEmbeddingProviderFailure{cause: &embeddingcontract.ProviderError{
		FailureCode: "provider_response_invalid", FailureClass: "provider_action_required", StatusCode: 422,
	}})
	processor.logRememberFailure(input, "attempt", time.Now(), "commit", "database_failure", "corr", context.DeadlineExceeded)
	processor.logRememberFailure(input, "attempt", time.Now(), "assessment", "provider_unavailable", "corr", errors.New("assessor failed"))
	require.Equal(t, []string{
		"remember_processing_failed", "remember_processing_failed", "remember_processing_failed",
		"remember_processing_failed", "remember_processing_failed",
	}, logger.errors)
}

func TestRememberAttemptStatusValidatesAndFillsDefaults(t *testing.T) {
	_, err := rememberAttemptStatus(nil)
	require.EqualError(t, err, "remember processor: attempt is required")
	status, err := rememberAttemptStatus(&repository.RememberAttempt{AttemptID: "attempt", PublicResult: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, "attempt", status.SubmissionID)
	require.Equal(t, "remember", status.SubmissionKind)
	require.Empty(t, status.ProcessingState)
	require.Empty(t, status.Evidence)
	require.Empty(t, status.RelationshipResults)
	require.Empty(t, status.Errors)
	_, err = rememberAttemptStatus(&repository.RememberAttempt{PublicResult: map[string]any{"evidence": make(chan int)}})
	require.Error(t, err)
	_, err = rememberAttemptStatus(&repository.RememberAttempt{PublicResult: map[string]any{"evidence": "invalid"}})
	require.Error(t, err)
	require.Equal(t, "first", firstNonEmptyString("", " first ", "second"))
	require.Empty(t, firstNonEmptyString("", "  "))
}

func TestRememberFailureHelpersCoverReplayAndInputConversions(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "key", RequestHash: "hash",
		Evidence: []rememberapp.EvidenceInput{{
			Content: "fact", ContentHash: "sha256:fact", SourceType: "note", Authority: "user", SourceRef: "source",
			SourceKey: "key", SourceRevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash: "sha256:source", SourceRevisionEnvelope: map[string]any{"revision": "rev-2"},
			SupersedesEvidenceIDs: []string{"old"}, Labels: []string{"label"}, Metadata: map[string]any{"kind": "test"},
			InitialEvent: &rememberapp.SecurityEventDraft{
				EventKind: "submission", Decision: "pass", Reason: "safe", Metadata: map[string]any{"source": "scanner"},
				Signals: []rememberapp.SecuritySignalInput{{Kind: "markup", Severity: "low", SpanStart: 1, SpanEnd: 2, Metadata: map[string]any{"rule": "x"}}},
			},
		}},
	}
	snapshot, _ := rememberAssessmentSnapshot(input, "attempt")
	converted := rememberEvidenceInputsForCommit(input, snapshot)
	require.Len(t, converted, 1)
	require.Equal(t, snapshot.Evidence[0].FragmentID, converted[0].FragmentID)
	require.Equal(t, "key:evidence:0", converted[0].IdempotencyKey)
	require.Equal(t, "submission", converted[0].InitialEvent.EventKind)
	require.Equal(t, "markup", converted[0].InitialEvent.Signals[0].Kind)
	require.Nil(t, rememberEvidenceInputs(nil))
	require.Equal(t, "submission_policy_rejected", rememberFailureNotStoredReason(rememberapp.SubmissionErrorPolicyRejected))
	require.Equal(t, "stale_input", rememberFailureNotStoredReason(rememberapp.SubmissionErrorStaleInput))
	require.Equal(t, "internal_failure", rememberFailureNotStoredReason(rememberapp.SubmissionErrorDatabaseFailure))
	require.Empty(t, rememberFailureRelationshipRefs(nil))
	require.Equal(t, []string{"a", ""}, rememberFailureRelationshipRefs(map[string]any{
		"relationship_hints": []any{map[string]any{"ref": " a "}, "not-an-object"},
	}))
	require.Equal(t, []string{"b"}, rememberFailureRelationshipRefs(map[string]any{
		"relationships": []map[string]any{{"ref": " b "}},
	}))
	require.Empty(t, rememberFailureRelationshipRefs(map[string]any{"relationships": "invalid"}))

	var nilStale *rememberStaleInputError
	require.Equal(t, []error{rememberapp.ErrRememberStaleInput}, nilStale.Unwrap())
	require.ErrorIs(t, newRememberStaleInputError(nil), rememberapp.ErrRememberStaleInput)
	staleCause := errors.New("source changed")
	stale := newRememberStaleInputError(staleCause)
	require.ErrorIs(t, stale, rememberapp.ErrRememberStaleInput)
	require.ErrorIs(t, stale, staleCause)
	require.ErrorIs(t, normalizeRememberFailureWithClassifier(errors.New("adapter stale"), func(error) bool { return true }), rememberapp.ErrRememberStaleInput)
	require.Equal(t, rememberapp.ErrRememberPersistence, rememberFailurePersistenceError(nil))

	winner := &repository.RememberAttempt{
		AttemptID: "winner", RequestHash: "hash", ContractVersion: domain.ContractVersion, Outcome: "completed",
		PublicResult: map[string]any{
			"contract_version": domain.ContractVersion, "submission_id": "winner", "submission_kind": "remember",
			"processing_state": "completed", "search_state": "current", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
		},
	}
	ledger := &rememberReplayFailureLedgerStub{rememberFailureLedgerStub: &rememberFailureLedgerStub{load: winner}, failureErr: repository.ErrRememberReplay}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	snapshot, _ = rememberAssessmentSnapshot(input, "attempt")
	status, err := processor.recordRememberFailure(context.Background(), input, "attempt", snapshot, time.Now(), "assessment", 0, rememberapp.ErrRememberProviderUnavailable)
	require.NoError(t, err)
	require.Equal(t, "winner", status.SubmissionID)
}

type processorEmbeddingProviderStub struct {
	available bool
	model     string
	vectors   [][]float32
	err       error
	called    int
}

type rememberReplayFailureLedgerStub struct {
	*rememberFailureLedgerStub
	failureErr error
}

func (s *rememberReplayFailureLedgerStub) RecordRememberFailure(context.Context, repository.RememberFailureRecordInput) error {
	return s.failureErr
}

type rememberPipelineLedgerStub struct {
	*rememberFailureLedgerStub
	duplicatePlan    *repository.RememberDuplicateEmbeddingPlan
	duplicatePlanErr error
	resolveErr       error
	plan             *repository.InlineEmbeddingPlan
	planErr          error
	commitResult     *repository.SynchronousRememberCommitResult
	commitErr        error
}

func (s *rememberPipelineLedgerStub) PlanRememberDuplicateEmbeddings(context.Context, repository.RememberDuplicateCandidateInput) (*repository.RememberDuplicateEmbeddingPlan, error) {
	if s.duplicatePlanErr != nil {
		return nil, s.duplicatePlanErr
	}
	if s.duplicatePlan != nil {
		return s.duplicatePlan, nil
	}
	return &repository.RememberDuplicateEmbeddingPlan{}, nil
}

func (s *rememberPipelineLedgerStub) ResolveRememberDuplicateCandidates(context.Context, repository.RememberDuplicateCandidateInput, []repository.InlineEmbeddingResult) (*repository.RememberDuplicateResolutionResult, error) {
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return &repository.RememberDuplicateResolutionResult{}, nil
}

func (s *rememberPipelineLedgerStub) PlanRememberEmbeddings(context.Context, knowledgecontract.SynchronousRememberCommitInput) (*repository.InlineEmbeddingPlan, error) {
	if s.planErr != nil {
		return nil, s.planErr
	}
	if s.plan != nil {
		return s.plan, nil
	}
	return &repository.InlineEmbeddingPlan{}, nil
}

func (s *rememberPipelineLedgerStub) CommitRememberWithEmbeddings(context.Context, knowledgecontract.SynchronousRememberCommitInput, []repository.InlineEmbeddingResult) (*repository.SynchronousRememberCommitResult, error) {
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	return s.commitResult, nil
}

type processorAssessmentCatalogStub struct{}

func (*processorAssessmentCatalogStub) ListSubmissionAssessmentEntityCatalog(context.Context, knowledgecontract.SubmissionAssessmentEntityCatalogInput) (knowledgecontract.SubmissionAssessmentEntityCatalogResult, error) {
	return knowledgecontract.SubmissionAssessmentEntityCatalogResult{Complete: true}, nil
}

func (*processorAssessmentCatalogStub) ResolveSemanticReviewPredicateCandidates(context.Context, knowledgecontract.SemanticReviewPredicateResolutionInput) ([]knowledgecontract.SemanticReviewPredicateResolution, error) {
	return nil, nil
}

func (*processorAssessmentCatalogStub) ListSemanticAssessmentPredicateOptions(context.Context, knowledgecontract.SemanticAssessmentPredicateOptionsInput) ([]knowledgecontract.SemanticReviewPredicateCandidate, error) {
	return nil, nil
}

type processorAssessmentSessionStub struct{}

func (*processorAssessmentSessionStub) SessionID() string { return "processor-assessment" }

type processorAssessmentProviderStub struct{}

func (*processorAssessmentProviderStub) Assess(_ context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	security := make([]assessor.SemanticAssessmentEvidenceSecurityResult, 0, len(request.Evidence))
	for _, evidence := range request.Evidence {
		security = append(security, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
	}
	return &processorAssessmentSessionStub{}, assessor.SemanticAssessmentTurn{
		Response: assessor.SemanticAssessmentResponse{
			RequestID: request.RequestID, EvidenceSecurityResults: security,
			EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{},
			EvidenceConflictResults:    []assessor.SemanticAssessmentEvidenceConflictResult{},
			EntityResults:              []assessor.SemanticAssessmentEntityResult{},
			RelationshipResults:        []assessor.SemanticAssessmentRelationshipResult{},
		},
	}, nil
}

func (*processorAssessmentProviderStub) Repair(context.Context, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	return assessor.SemanticAssessmentTurn{}, errors.New("unexpected assessment repair")
}

func (*processorAssessmentProviderStub) ModelName() string { return "processor-assessment-model" }

var _ embeddingcontract.EmbeddingProviderInterface = (*processorEmbeddingProviderStub)(nil)

func (p *processorEmbeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, p.model, p.err
}

func (p *processorEmbeddingProviderStub) EmbedBatch(_ context.Context, _ []string) ([][]float32, string, error) {
	p.called++
	return p.vectors, p.model, p.err
}

func (p *processorEmbeddingProviderStub) ModelName() string { return p.model }
func (p *processorEmbeddingProviderStub) Dimensions() int   { return 2 }
func (p *processorEmbeddingProviderStub) IsAvailable() bool { return p.available }

func processorEmbeddingDocument(dimensions int) knowledgecontract.SearchDocumentForEmbedding {
	return knowledgecontract.SearchDocumentForEmbedding{
		SearchDocumentResult: knowledgecontract.SearchDocumentResult{
			TeamID: "team", OwnerProfileID: "owner", SearchDocumentID: "doc-id", SourceKind: "evidence",
			SourceID: "source-id", SourceVersion: 3, ProjectionFormat: 1, ProjectionGenerationID: "projection",
			DocumentVersion: 4, EmbeddingContractID: "contract", EmbeddingDimensions: dimensions, SpaceID: "space", SpaceGeneration: 5,
		},
		DocumentText: "document text", DocumentHash: "hash", StoredDocumentHash: "stored-hash",
	}
}

func TestEmbedSearchDocumentBatchValidatesProviderAndVectors(t *testing.T) {
	processor := &rememberSynchronousProcessor{}
	require.Empty(t, mustEmbedDocuments(t, processor, nil, "model", nil))
	tooMany := make([]knowledgecontract.SearchDocumentForEmbedding, 257)
	_, err := processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", tooMany)
	require.ErrorIs(t, err, rememberapp.ErrRememberInputBudgetExceeded)

	document := processorEmbeddingDocument(2)
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)

	provider := &processorEmbeddingProviderStub{available: true, model: "model", vectors: [][]float32{{1, 2}}}
	processor.embedder = provider
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "other", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	require.Zero(t, provider.called)

	provider.err = errors.New("provider unavailable")
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	var providerFailure *rememberEmbeddingProviderFailure
	require.ErrorAs(t, err, &providerFailure)
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
	provider.err = context.Canceled
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberRequestCancelled)

	provider.err = errors.New("provider timeout")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	_, err = processor.embedSearchDocumentBatch(expired, "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberRequestTimeout)

	provider.err = nil
	provider.vectors = [][]float32{}
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	provider.vectors = [][]float32{{1, 2}}
	provider.model = "other"
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	provider.model = "model"
	provider.vectors = [][]float32{{1}}
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	provider.vectors = [][]float32{{float32(math.NaN()), 2}}
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
	provider.vectors = [][]float32{{float32(math.Inf(1)), 2}}
	_, err = processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)

	provider.vectors = [][]float32{{1, 2}}
	completed, err := processor.embedSearchDocumentBatch(context.Background(), "team", "owner", "model", []knowledgecontract.SearchDocumentForEmbedding{document})
	require.NoError(t, err)
	require.Len(t, completed, 1)
	require.Equal(t, document.SearchDocumentID, completed[0].SearchDocumentID)
	require.Equal(t, document.DocumentHash, completed[0].DocumentHash)
	require.Equal(t, []float32{1, 2}, completed[0].Embedding)
}

func mustEmbedDocuments(t *testing.T, processor *rememberSynchronousProcessor, provider embeddingcontract.EmbeddingProviderInterface, model string, documents []knowledgecontract.SearchDocumentForEmbedding) []knowledgecontract.SearchDocumentEmbedding {
	t.Helper()
	processor.embedder = provider
	result, err := processor.embedSearchDocumentBatch(context.Background(), "team", "owner", model, documents)
	require.NoError(t, err)
	return result
}

func TestRememberFailureCodeMapsAllTerminalCategories(t *testing.T) {
	cases := []struct {
		phase string
		err   error
		want  rememberapp.SubmissionErrorCode
	}{
		{"assessment", rememberapp.ErrRememberPolicyRejected, rememberapp.SubmissionErrorPolicyRejected},
		{"assessment", rememberapp.ErrEvidenceSecurityRejected, rememberapp.SubmissionErrorPolicyRejected},
		{"assessment", rememberapp.ErrEncodedEvidenceNotAllowed, rememberapp.SubmissionErrorPolicyRejected},
		{"assessment", rememberapp.ErrRememberRequestTimeout, rememberapp.SubmissionErrorRequestTimeout},
		{"assessment", context.DeadlineExceeded, rememberapp.SubmissionErrorRequestTimeout},
		{"assessment", rememberapp.ErrRememberRequestCancelled, rememberapp.SubmissionErrorRequestCancelled},
		{"assessment", context.Canceled, rememberapp.SubmissionErrorRequestCancelled},
		{"assessment", rememberapp.ErrRememberDatabaseFailure, rememberapp.SubmissionErrorDatabaseFailure},
		{"embedding", &rememberEmbeddingPlanFailure{cause: repository.ErrInlineEmbeddingPlanTooLarge}, rememberapp.SubmissionErrorInputBudgetExceeded},
		{"embedding", &rememberEmbeddingPlanFailure{cause: repository.ErrSearchContractMismatch}, rememberapp.SubmissionErrorConfigurationInvalid},
		{"embedding", &rememberEmbeddingPlanFailure{cause: repository.ErrInlineEmbeddingPlanMismatch}, rememberapp.SubmissionErrorInternalFailure},
		{"embedding", &rememberEmbeddingPlanFailure{cause: errors.New("database")}, rememberapp.SubmissionErrorDatabaseFailure},
		{"embedding", &rememberEmbeddingConfigurationFailure{}, rememberapp.SubmissionErrorConfigurationInvalid},
		{"embedding", &rememberEmbeddingProviderFailure{cause: &embeddingcontract.ProviderError{FailureCode: "provider_response_invalid", FailureClass: "provider_action_required"}}, rememberapp.SubmissionErrorEmbeddingResponseInvalid},
		{"embedding", rememberapp.ErrRememberEmbeddingUnavailable, rememberapp.SubmissionErrorEmbeddingUnavailable},
		{"embedding", rememberapp.ErrRememberEmbeddingInvalid, rememberapp.SubmissionErrorEmbeddingResponseInvalid},
		{"assessment", rememberapp.ErrRememberProviderUnavailable, rememberapp.SubmissionErrorProviderUnavailable},
		{"assessment", rememberapp.ErrRememberProviderResponseInvalid, rememberapp.SubmissionErrorProviderResponseInvalid},
		{"assessment", rememberapp.ErrRememberInputBudgetExceeded, rememberapp.SubmissionErrorInputBudgetExceeded},
		{"commit", rememberapp.ErrRememberCommitConflict, rememberapp.SubmissionErrorCommitConflict},
		{"commit", repository.ErrSearchStaleVersion, rememberapp.SubmissionErrorCommitConflict},
		{"commit", repository.ErrRememberDuplicateCandidateStale, rememberapp.SubmissionErrorStaleInput},
		{"commit", repository.ErrSourceRevisionConflict, rememberapp.SubmissionErrorStaleInput},
		{"commit", repository.ErrEvidenceConflictStaleInput, rememberapp.SubmissionErrorStaleInput},
		{"commit", rememberapp.ErrSourceRevisionConflict, rememberapp.SubmissionErrorStaleInput},
		{"other", errors.New("other"), rememberapp.SubmissionErrorDatabaseFailure},
	}
	for _, test := range cases {
		t.Run(strings.ReplaceAll(string(test.want), "_", "-"), func(t *testing.T) {
			require.Equal(t, test.want, rememberFailureCode(test.phase, test.err))
		})
	}
}

func TestRememberDiagnosticsRecordsUncapturedPhasesAndDerivedSecurityHash(t *testing.T) {
	input := rememberapp.RememberProcessRequest{
		OriginalRequest:  []byte(`{"evidence":[{"content":"secret"}]}`),
		Evidence:         []rememberapp.EvidenceInput{{Content: "secret"}},
		SecurityRejected: true,
	}
	items := rememberFailureDiagnosticsWithCapture(input, nil, nil, nil, true, true)
	require.Len(t, items, 3)
	require.Equal(t, "hash_only", items[0].CaptureState)
	require.Contains(t, string(items[0].RequestBody), "request_sha256")
	require.Equal(t, "provider_not_called", items[1].CaptureState)
	require.Equal(t, "not_captured", items[2].CaptureState)

	items = rememberFailureDiagnosticsWithCapture(rememberapp.RememberProcessRequest{}, nil, []modelprovider.ProviderExchange{{
		Component: "provider", RequestBody: []byte("request"), ResponseBody: []byte("response"),
	}}, nil, true, true)
	require.Len(t, items, 3)
	require.Equal(t, "not_captured", items[0].CaptureState)
	require.Equal(t, "captured", items[1].Outcome)
	require.Equal(t, "not_captured", items[2].CaptureState)
	require.False(t, items[1].CapturedAt.IsZero())

	var nilRecorder *rememberExchangeRecorder
	nilRecorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{})
	require.Nil(t, nilRecorder.Snapshot())
}
