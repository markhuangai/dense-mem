package serverapp

import (
	"context"
	"encoding/json"
	"errors"
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
	require.Equal(t, "provider_action_required", logger.attrs["failure_class"])
	require.Equal(t, "provider_authentication_failed", logger.attrs["failure_code"])
	require.Equal(t, 401, logger.attrs["provider_status_code"])
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
