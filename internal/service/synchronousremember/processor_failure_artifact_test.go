package synchronousremember

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousFailurePersistsOneScrubbedFailureArtifact(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousMalformedProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "failure-artifact", "failure-artifact-hash"))
	require.Error(t, err)
	require.Len(t, ledger.failureInput.Artifacts, 1)
	artifact := ledger.failureInput.Artifacts[0]
	require.Equal(t, "failure", artifact.ArtifactKind)
	require.Equal(t, "application/json", artifact.ContentType)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(artifact.Content, &payload))
	require.JSONEq(t, `{"phase":"assessment","error_code":"provider_response_invalid"}`, string(artifact.Content))
	require.Equal(t, "assessment", payload["phase"])
	require.Equal(t, string(remember.TerminalErrorProviderResponseInvalid), payload["error_code"])
	require.NotContains(t, string(artifact.Content), "evidence")
	require.Equal(t, 1, ledger.recordFailureCalls)
}

func TestSynchronousRejectedAndQuarantinedOutcomesDoNotPersistFailureArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
	}{
		{name: "rejected"},
		{name: "quarantined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			teamID, ownerID := uuid.NewString(), uuid.NewString()
			input := synchronousPipelineRememberRequest(teamID, ownerID, "no-failure-artifact-"+test.name, "hash-"+test.name)
			provider := &synchronousPipelineProvider{}
			if test.name == "rejected" {
				provider.response = synchronousPipelineUnsupportedResponse
			}
			if test.name == "quarantined" {
				input.SecurityRejected = true
			}
			ledger := &synchronousPipelineLedger{}
			processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
				Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
				Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
			})
			_, _ = processor.ProcessRemember(context.Background(), input)
			require.Empty(t, ledger.failureInput.Artifacts)
			require.Zero(t, ledger.recordFailureCalls)
		})
	}
}

func TestSynchronousCompletedAndReplayedOutcomesDoNotPersistFailureArtifacts(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		teamID, ownerID := uuid.NewString(), uuid.NewString()
		ledger := &synchronousPipelineLedger{}
		processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
			Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
			Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
		})

		status, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "completed-no-artifact", "completed-no-artifact-hash"))
		require.NoError(t, err)
		require.Equal(t, "completed", status.ProcessingState)
		require.Zero(t, ledger.recordFailureCalls)
		require.Empty(t, ledger.failureInput.Artifacts)
	})

	t.Run("replayed", func(t *testing.T) {
		teamID, ownerID := uuid.NewString(), uuid.NewString()
		input := synchronousPipelineRememberRequest(teamID, ownerID, "replayed-no-artifact", "replayed-no-artifact-hash")
		winnerID := uuid.NewString()
		winner := synchronousTerminalBase(input, winnerID, nil)
		winner.ProcessingState = string(remember.TerminalProcessingCompleted)
		winner.SearchState = string(remember.TerminalSearchCurrent)
		public, err := terminalMap(winner)
		require.NoError(t, err)
		ledger := &synchronousPipelineLedger{loadResult: &repository.RememberAttempt{
			AttemptID: winnerID, RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion,
			Outcome: "replayed", PublicResult: public,
		}}
		processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
			Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
			Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
		})

		status, err := processor.ProcessRemember(context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, winnerID, status.SubmissionID)
		require.Zero(t, ledger.recordFailureCalls)
		require.Empty(t, ledger.failureInput.Artifacts)
	})
}
