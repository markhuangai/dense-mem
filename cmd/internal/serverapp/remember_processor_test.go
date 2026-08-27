package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberAssessmentSnapshotAllocatesRequestOwnedIDs(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	snapshot, run := rememberAssessmentSnapshot(rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: uuid.NewString(), SpaceGeneration: 4,
		Evidence: []rememberapp.EvidenceInput{{Content: "first", ContentHash: "hash-1", SourceKey: "source-1", SourceRevisionToken: "rev-1"}, {Content: "second"}},
	}, uuid.NewString())

	require.Len(t, snapshot.Evidence, 2)
	require.Len(t, snapshot.Items, 2)
	require.Equal(t, teamID, run.TeamID)
	require.Equal(t, ownerID, run.OwnerProfileID)
	require.Equal(t, int64(4), run.SpaceGeneration)
	require.NotEmpty(t, snapshot.Evidence[0].FragmentID)
	require.Equal(t, snapshot.Evidence[0].FragmentID, snapshot.Items[0].Fragment.FragmentID)
	require.NotEqual(t, snapshot.Items[0].ItemID, snapshot.Items[1].ItemID)
	require.Empty(t, snapshot.Evidence[0].SourceID)
	require.Empty(t, snapshot.Evidence[0].SourceRevisionID)
	commitEvidence := rememberEvidenceInputsForCommit(rememberapp.RememberProcessRequest{
		IdempotencyKey: "remember-source-lineage",
		Evidence:       []rememberapp.EvidenceInput{{SourceKey: "source-1", SourceRevisionToken: "rev-1"}},
	}, snapshot)
	require.Equal(t, "source-1", commitEvidence[0].SourceKey)
	require.Equal(t, "rev-1", commitEvidence[0].SourceRevisionToken)
}

func TestRememberProcessorEmbedsOneValidatedBatch(t *testing.T) {
	processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{
		model: "embed-model", vectors: [][]float32{{1, 2, 3}, {4, 5, 6}},
	}}
	documents := []repository.SearchDocumentForEmbedding{
		{SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document-a", EmbeddingDimensions: 3}, DocumentText: "first"},
		{SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document-b", EmbeddingDimensions: 3}, DocumentText: "second"},
	}

	result, err := processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), documents)
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, []float32{1, 2, 3}, result[0].Embedding)
	require.Equal(t, []string{"first", "second"}, processor.embedder.(*rememberProcessorEmbedderStub).texts)
	require.True(t, processor.embedder.(*rememberProcessorEmbedderStub).hasOperation)
}

func TestRememberAssessmentTerminalOutcomePrioritizesSecurity(t *testing.T) {
	prepared := &memoryservice.SynchronousAssessmentResult{
		Response: assessor.SemanticAssessmentResponse{
			SecuritySignals: []assessor.SemanticAssessmentSecuritySignal{{EvidenceID: "evidence:0"}},
		},
	}

	require.Equal(t, "quarantined", rememberAssessmentTerminalOutcome(prepared, true))
	require.Equal(t, "quarantined", rememberAssessmentTerminalOutcome(prepared, false))
	require.Equal(t, "rejected", rememberAssessmentTerminalOutcome(&memoryservice.SynchronousAssessmentResult{}, true))
	require.Empty(t, rememberAssessmentTerminalOutcome(&memoryservice.SynchronousAssessmentResult{}, false))
}

func TestRememberPreflightCommitFailureRecordsAuthoritativeAttempt(t *testing.T) {
	ledger := &rememberProcessorLedgerStub{preflightErr: context.DeadlineExceeded}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IdempotencyKey: "preflight-timeout",
		RequestHash: "request-hash", SecurityRejected: true,
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "preflight-correlation"}},
		Evidence: []rememberapp.EvidenceInput{{Content: "rejected evidence", ContentHash: "content-hash"}},
	}

	result, err := processor.ProcessRemember(context.Background(), input)

	require.Nil(t, result)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, processErr.Status)
	require.Equal(t, ledger.failure.AttemptID, processErr.Status.SubmissionID)
	require.Equal(t, "failed", processErr.Status.ProcessingState)
	require.Equal(t, string(rememberapp.SubmissionErrorRequestTimeout), processErr.Status.Errors[0].Code)
	require.Equal(t, "commit", ledger.failure.FailedPhase)
	require.Equal(t, string(rememberapp.SubmissionErrorRequestTimeout), ledger.failure.ErrorCode)
	require.Equal(t, "preflight-correlation", ledger.failure.CorrelationID)
	require.Len(t, ledger.failure.Artifacts, 2)
}

func TestRememberAttemptLookupFailureRecordsDatabaseFailure(t *testing.T) {
	lookupErr := errors.New("attempt lookup failed")
	ledger := &rememberProcessorLedgerStub{lookupErr: lookupErr}
	processor := &rememberSynchronousProcessor{ledger: ledger}
	input := rememberapp.RememberProcessRequest{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IdempotencyKey: "lookup-failure",
		RequestHash: "request-hash", Evidence: []rememberapp.EvidenceInput{{Content: "lookup evidence", ContentHash: "content-hash"}},
	}

	result, err := processor.ProcessRemember(context.Background(), input)

	require.Nil(t, result)
	require.ErrorIs(t, err, lookupErr)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.NotNil(t, processErr.Status)
	require.Equal(t, ledger.failure.AttemptID, processErr.Status.SubmissionID)
	require.Equal(t, "failed", processErr.Status.ProcessingState)
	require.Equal(t, string(rememberapp.SubmissionErrorDatabaseFailure), processErr.Status.Errors[0].Code)
	require.Equal(t, "commit", ledger.failure.FailedPhase)
	require.Equal(t, string(rememberapp.SubmissionErrorDatabaseFailure), ledger.failure.ErrorCode)
}

func TestRememberProcessorRequiresEmbedderOnlyForNonemptyPlan(t *testing.T) {
	processor := &rememberSynchronousProcessor{}
	embeddings, err := processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), nil)
	require.NoError(t, err)
	require.Empty(t, embeddings)

	_, err = processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), []repository.SearchDocumentForEmbedding{{
		SearchDocumentResult: repository.SearchDocumentResult{EmbeddingDimensions: 2}, DocumentText: "text",
	}})
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
}

func TestRememberProcessorRejectsInvalidEmbeddingResponsesBeforeCommit(t *testing.T) {
	for _, test := range []struct {
		name    string
		vectors [][]float32
	}{
		{name: "wrong dimensions", vectors: [][]float32{{1}}},
		{name: "non finite", vectors: [][]float32{{float32(math.NaN()), 2}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{model: "embed-model", vectors: test.vectors}}
			_, err := processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), []repository.SearchDocumentForEmbedding{{
				SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document", EmbeddingDimensions: 2}, DocumentText: "text",
			}})
			require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingInvalid)
		})
	}
}

func TestRememberProcessorPreservesEmbeddingCancellation(t *testing.T) {
	processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{
		model: "embed-model", vectors: [][]float32{{1, 2}}, err: context.Canceled,
	}}

	_, err := processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), []repository.SearchDocumentForEmbedding{{
		SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document", EmbeddingDimensions: 2}, DocumentText: "text",
	}})

	require.ErrorIs(t, err, rememberapp.ErrRememberRequestCancelled)
	require.NotErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
}

func TestRememberProcessorPreservesProviderFailureForOperatorLogging(t *testing.T) {
	providerErr := &embedding.ProviderHTTPError{Status: 401, Code: "invalid_api_key", Type: "authentication_error"}
	processor := &rememberSynchronousProcessor{embedder: &rememberProcessorEmbedderStub{
		model: "embed-model", err: providerErr,
	}}

	_, err := processor.embedSearchDocumentBatch(context.Background(), uuid.NewString(), uuid.NewString(), []repository.SearchDocumentForEmbedding{{
		SearchDocumentResult: repository.SearchDocumentResult{SearchDocumentID: "document", EmbeddingDimensions: 2}, DocumentText: "text",
	}})

	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
	require.ErrorIs(t, err, providerErr)
	require.Contains(t, err.Error(), "status=401")
}

func TestRememberFailureLogIncludesCorrelationAndBoundedProviderTrace(t *testing.T) {
	logger := &rememberProcessorLoggerStub{}
	processor := &rememberSynchronousProcessor{logger: logger}
	teamID, profileID, attemptID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	providerErr := &embedding.ProviderHTTPError{Status: 401, Code: "invalid_api_key", Type: "authentication_error"}
	failure := &rememberEmbeddingProviderFailure{cause: providerErr}
	input := rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: profileID,
		Metadata: map[string]any{"actor": map[string]any{"correlation_id": "corr-remember-failure"}},
	}

	processor.logRememberFailure(
		input, attemptID, time.Now().Add(-25*time.Millisecond), "embedding",
		string(rememberapp.SubmissionErrorEmbeddingUnavailable), "corr-remember-failure", failure,
	)

	require.Equal(t, "remember_processing_failed", logger.message)
	require.ErrorIs(t, logger.err, providerErr)
	require.Equal(t, teamID, logger.attrs["team_id"])
	require.Equal(t, profileID, logger.attrs["profile_id"])
	require.Equal(t, "corr-remember-failure", logger.attrs["correlation_id"])
	require.Equal(t, attemptID, logger.attrs["reference_id"])
	require.Equal(t, "embedding", logger.attrs["failed_phase"])
	require.Equal(t, "provider_call", logger.attrs["failure_source"])
	require.Equal(t, "provider_action_required", logger.attrs["failure_class"])
	require.Equal(t, "provider_authentication_failed", logger.attrs["failure_code"])
	require.Equal(t, 401, logger.attrs["provider_status_code"])
}

func TestRememberFailureLogClassifiesEmbeddingPlanWithoutExposingCause(t *testing.T) {
	logger := &rememberProcessorLoggerStub{}
	processor := &rememberSynchronousProcessor{logger: logger}
	rawDatabaseError := errors.New("postgres password=do-not-log")

	processor.logRememberFailure(
		rememberapp.RememberProcessRequest{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()},
		uuid.NewString(), time.Now(), "embedding", string(rememberapp.SubmissionErrorEmbeddingUnavailable), "corr-plan",
		&rememberEmbeddingPlanFailure{cause: rawDatabaseError},
	)

	require.Equal(t, "embedding_plan", logger.attrs["failure_source"])
	require.Equal(t, "internal", logger.attrs["failure_class"])
	require.Equal(t, "embedding_plan_failed", logger.attrs["failure_code"])
	require.EqualError(t, logger.err, "remember embedding plan failed")
	require.NotContains(t, logger.err.Error(), rawDatabaseError.Error())
}

func TestRememberFailureLogClassifiesProviderConfiguration(t *testing.T) {
	logger := &rememberProcessorLoggerStub{}
	processor := &rememberSynchronousProcessor{logger: logger}

	processor.logRememberFailure(
		rememberapp.RememberProcessRequest{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()},
		uuid.NewString(), time.Now(), "embedding", string(rememberapp.SubmissionErrorEmbeddingUnavailable), "corr-config",
		&rememberEmbeddingConfigurationFailure{},
	)

	require.Equal(t, "provider_configuration", logger.attrs["failure_source"])
	require.Equal(t, "configuration", logger.attrs["failure_class"])
	require.Equal(t, "embedding_provider_not_configured", logger.attrs["failure_code"])
	require.EqualError(t, logger.err, "remember embedding provider configuration is invalid")
}

func TestRememberFailureLogClassifiesCommitWithoutExposingCause(t *testing.T) {
	for _, test := range []struct {
		name         string
		failure      error
		failureClass string
		failureCode  string
	}{
		{name: "embedding plan mismatch", failure: fmt.Errorf("repository commit: %w", repository.ErrInlineEmbeddingPlanMismatch), failureClass: "data_contract", failureCode: "embedding_plan_mismatch"},
		{name: "database failure", failure: errors.New("postgres password=do-not-log"), failureClass: "database", failureCode: "semantic_commit_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := &rememberProcessorLoggerStub{}
			processor := &rememberSynchronousProcessor{logger: logger}

			processor.logRememberFailure(
				rememberapp.RememberProcessRequest{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()},
				uuid.NewString(), time.Now(), "commit", string(rememberapp.SubmissionErrorDatabaseFailure), "corr-commit", test.failure,
			)

			require.Equal(t, "semantic_commit", logger.attrs["failure_source"])
			require.Equal(t, test.failureClass, logger.attrs["failure_class"])
			require.Equal(t, test.failureCode, logger.attrs["failure_code"])
			require.EqualError(t, logger.err, "remember semantic commit failed")
			require.NotContains(t, logger.err.Error(), test.failure.Error())
		})
	}
}

func TestRememberFailureCodeDistinguishesEmbeddingBoundaries(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want rememberapp.SubmissionErrorCode
	}{
		{name: "plan mismatch", err: &rememberEmbeddingPlanFailure{cause: repository.ErrInlineEmbeddingPlanMismatch}, want: rememberapp.SubmissionErrorInternalFailure},
		{name: "search contract", err: &rememberEmbeddingPlanFailure{cause: repository.ErrSearchContractMismatch}, want: rememberapp.SubmissionErrorConfigurationInvalid},
		{name: "plan database", err: &rememberEmbeddingPlanFailure{cause: errors.New("database query failed")}, want: rememberapp.SubmissionErrorDatabaseFailure},
		{name: "plan bound", err: &rememberEmbeddingPlanFailure{cause: repository.ErrInlineEmbeddingPlanTooLarge}, want: rememberapp.SubmissionErrorInputBudgetExceeded},
		{name: "provider configuration", err: &rememberEmbeddingConfigurationFailure{}, want: rememberapp.SubmissionErrorConfigurationInvalid},
		{name: "provider call", err: &rememberEmbeddingProviderFailure{cause: errors.New("provider failed")}, want: rememberapp.SubmissionErrorEmbeddingUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, rememberFailureCode("embedding", test.err))
		})
	}
}

func TestNormalizeRememberCommitFailureMapsSearchFences(t *testing.T) {
	for _, failure := range []error{repository.ErrSearchStaleVersion, repository.ErrSearchContractMismatch} {
		normalized := normalizeRememberCommitFailure(fmt.Errorf("repository commit: %w", failure))
		require.ErrorIs(t, normalized, rememberapp.ErrRememberCommitConflict)
		require.Equal(t, rememberapp.SubmissionErrorCommitConflict, rememberFailureCode("commit", normalized))
		failureClass, failureCode := rememberCommitFailureMetadata(normalized)
		require.Equal(t, "fence_conflict", failureClass)
		require.Equal(t, "search_state_changed", failureCode)
	}

	unrelated := errors.New("database failed")
	require.ErrorIs(t, normalizeRememberCommitFailure(unrelated), unrelated)
}

func TestRememberFailureNormalizationMapsCallerOwnedFences(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "conflict context", err: repository.ErrConflictContextStale},
		{name: "correction target", err: repository.ErrCorrectionTargetStale},
		{name: "exact reference", err: repository.ErrRememberExactReferenceStale},
		{name: "semantic source", err: repository.ErrSemanticStaleSource},
		{name: "evidence lifecycle", err: repository.ErrEvidenceLifecycleConflict},
		{name: "repository source revision", err: repository.ErrSourceRevisionConflict},
		{name: "service source revision", err: rememberapp.ErrSourceRevisionConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeRememberFailure(test.err)
			require.ErrorIs(t, normalized, rememberapp.ErrRememberStaleInput)
			require.Equal(t, rememberapp.SubmissionErrorStaleInput, rememberFailureCode("commit", normalized))
		})
	}
}

func TestRememberFailureRecordLogDoesNotExposeDatabaseError(t *testing.T) {
	logger := &rememberProcessorLoggerStub{}
	processor := &rememberSynchronousProcessor{logger: logger}
	rawDatabaseError := errors.New("postgres password=do-not-log")

	processor.logRememberFailureRecordError(
		rememberapp.RememberProcessRequest{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()},
		uuid.NewString(), "embedding", string(rememberapp.SubmissionErrorEmbeddingUnavailable), "corr-recovery", rawDatabaseError,
	)

	require.Equal(t, "remember_failure_record_failed", logger.message)
	require.NotContains(t, logger.err.Error(), rawDatabaseError.Error())
	require.Equal(t, "persistence_failed", logger.attrs["recovery_error_code"])
}

func TestRememberFailurePersistenceErrorPreservesTypingAndCause(t *testing.T) {
	providerErr := &embedding.ProviderHTTPError{Status: 503}
	failure := &rememberEmbeddingProviderFailure{cause: providerErr}

	err := rememberFailurePersistenceError(failure)

	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	require.ErrorIs(t, err, rememberapp.ErrRememberEmbeddingUnavailable)
	require.ErrorIs(t, err, providerErr)
}

func TestRememberFailureRecoveryContextSurvivesRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	recoveryCtx, cancelRecovery := rememberFailureRecoveryContext(requestCtx)
	defer cancelRecovery()

	require.NoError(t, recoveryCtx.Err())
	deadline, ok := recoveryCtx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(10*time.Second), deadline, time.Second)
}

func TestRememberFailureRequestArtifactRedactsEvidence(t *testing.T) {
	rawEvidence := "credential=secret-token and prompt=do not retain this"
	attemptID := uuid.NewString()
	artifact, ok := rememberFailureRequestArtifact(attemptID, []repository.EvidenceFragment{{
		EvidenceIndex: 3,
		Content:       rawEvidence,
	}})

	require.True(t, ok)
	require.Equal(t, "request", artifact.ArtifactKind)
	require.NotContains(t, string(artifact.Content), rawEvidence)

	var payload struct {
		SubmissionID string `json:"submission_id"`
		Evidence     []struct {
			Index       int    `json:"index"`
			ContentHash string `json:"content_hash"`
		} `json:"evidence"`
	}
	require.NoError(t, json.Unmarshal(artifact.Content, &payload))
	require.Equal(t, attemptID, payload.SubmissionID)
	require.Equal(t, []struct {
		Index       int    `json:"index"`
		ContentHash string `json:"content_hash"`
	}{{Index: 3, ContentHash: "sha256:49308a379e8f21d308f3186e2f2ba80cf4b7981a96d7354d9069889fffba4796"}}, payload.Evidence)
}

func TestRememberAttemptReplayKeepsFailedStatusOnErrorPath(t *testing.T) {
	attemptID := uuid.NewString()
	status, err := rememberAttemptReplay(&repository.RememberAttempt{
		AttemptID: attemptID, Outcome: "failed",
		PublicResult: map[string]any{
			"submission_id":    attemptID,
			"processing_state": "failed",
			"errors": []any{map[string]any{
				"code": "provider_unavailable", "message": "the semantic assessor was unavailable",
				"retryable": true, "next_action": "retry_same_request",
				"remediation": "Retry the same request with the same idempotency_key after the transient failure clears.",
			}},
		},
	})

	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	require.Nil(t, status)
	require.NotNil(t, processErr.Status)
	require.Equal(t, attemptID, processErr.Status.SubmissionID)
	require.Equal(t, "failed", processErr.Status.ProcessingState)
	require.Equal(t, "provider_unavailable", processErr.Status.Errors[0].Code)
}

func TestRememberAttemptMatchesMigratedRequestHash(t *testing.T) {
	input := rememberapp.RememberProcessRequest{RequestHash: "v2.6.1-hash", MigratedRequestHash: "v2.6-hash"}

	require.True(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{
		RequestHash: "v2.6-hash", ContractVersion: domain.MigratedRememberRequestHashVersion,
	}, input))
	require.False(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{
		RequestHash: "v2.6-hash", ContractVersion: domain.ContractVersion,
	}, input))
	require.False(t, rememberAttemptMatchesRequest(&repository.RememberAttempt{
		RequestHash: "different", ContractVersion: domain.MigratedRememberRequestHashVersion,
	}, input))
}

type rememberProcessorLedgerStub struct {
	preflightErr error
	lookupErr    error
	failure      repository.RememberFailureRecordInput
}

func (s *rememberProcessorLedgerStub) LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return nil, repository.ErrRememberAttemptNotFound
}

func (s *rememberProcessorLedgerStub) CommitRememberPreflightQuarantine(context.Context, repository.SynchronousRememberCommitInput, string) (*repository.SynchronousRememberCommitResult, error) {
	return nil, s.preflightErr
}

func (s *rememberProcessorLedgerStub) RecordRememberFailure(_ context.Context, input repository.RememberFailureRecordInput) error {
	s.failure = input
	return nil
}

func (s *rememberProcessorLedgerStub) CommitRememberTerminal(context.Context, repository.SynchronousRememberCommitInput, string, string, []repository.SubmissionAssessmentSecurityQuarantineInput) (*repository.SynchronousRememberCommitResult, error) {
	panic("unexpected terminal commit")
}

func (s *rememberProcessorLedgerStub) PlanRememberEmbeddings(context.Context, repository.SynchronousRememberCommitInput) (*repository.InlineEmbeddingPlan, error) {
	panic("unexpected embedding plan")
}

func (s *rememberProcessorLedgerStub) CommitRememberWithEmbeddings(context.Context, repository.SynchronousRememberCommitInput, []repository.InlineEmbeddingResult) (*repository.SynchronousRememberCommitResult, error) {
	panic("unexpected embedding commit")
}

var _ embedding.EmbeddingProviderInterface = (*rememberProcessorEmbedderStub)(nil)

type rememberProcessorEmbedderStub struct {
	model        string
	vectors      [][]float32
	texts        []string
	err          error
	unavailable  bool
	hasOperation bool
}

func (s *rememberProcessorEmbedderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, s.model, nil
}

func (s *rememberProcessorEmbedderStub) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	s.texts = append([]string(nil), texts...)
	s.hasOperation = observability.HasAIOperation(ctx)
	return s.vectors, s.model, s.err
}

func (s *rememberProcessorEmbedderStub) ModelName() string { return s.model }
func (s *rememberProcessorEmbedderStub) Dimensions() int {
	if len(s.vectors) == 0 {
		return 0
	}
	return len(s.vectors[0])
}
func (s *rememberProcessorEmbedderStub) IsAvailable() bool { return !s.unavailable && s.model != "" }

type rememberProcessorLoggerStub struct {
	message string
	err     error
	attrs   map[string]any
}

func (s *rememberProcessorLoggerStub) Info(string, ...observability.LogAttr)  {}
func (s *rememberProcessorLoggerStub) Warn(string, ...observability.LogAttr)  {}
func (s *rememberProcessorLoggerStub) Debug(string, ...observability.LogAttr) {}
func (s *rememberProcessorLoggerStub) With(...observability.LogAttr) observability.LogProvider {
	return s
}
func (s *rememberProcessorLoggerStub) Error(message string, err error, attrs ...observability.LogAttr) {
	s.message = message
	s.err = err
	s.attrs = make(map[string]any, len(attrs))
	for _, attr := range attrs {
		s.attrs[attr.Key] = attr.Value
	}
}
