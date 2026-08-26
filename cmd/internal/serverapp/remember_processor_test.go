package serverapp

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
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
