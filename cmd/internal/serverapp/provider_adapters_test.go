package serverapp

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/verifier"
	"github.com/stretchr/testify/require"
)

func TestLegacyDreamGenerationResponsePreservesDiagnostics(t *testing.T) {
	response := legacyDreamGenerationResponse(verifier.DreamGenerationResponse{
		RequestID: "dream-request",
		Proposals: []verifier.DreamGenerationProposal{{
			PathRef: "path-1", PredicateRef: "predicate-1", Statement: "A may relate to C.",
		}},
		InputTokens: 11, OutputTokens: 7, ProviderTurns: 2,
	})

	require.Len(t, response.Proposals, 1)
	require.Equal(t, "path-1", response.Proposals[0].PathRef)
	require.Equal(t, 11, response.InputTokens)
	require.Equal(t, 7, response.OutputTokens)
	require.Equal(t, 2, response.ProviderTurns)
}

type semanticWriteEmbeddingProviderStub struct {
	err error
}

func (p semanticWriteEmbeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, "", p.err
}

func (p semanticWriteEmbeddingProviderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "model", p.err
}

func (semanticWriteEmbeddingProviderStub) ModelName() string { return "model" }
func (semanticWriteEmbeddingProviderStub) Dimensions() int   { return 2 }
func (semanticWriteEmbeddingProviderStub) IsAvailable() bool { return true }

func TestSemanticWriteProviderPreservesMalformedResponseClassification(t *testing.T) {
	provider := semanticWriteEmbeddingProviderStub{err: &embedding.ProviderError{
		Provider:     "openai",
		FailureClass: "provider_action_required",
		FailureCode:  "provider_response_invalid",
	}}
	adapter := semanticWriteProvider{provider: provider}
	plan := semanticwrite.Plan{
		Documents: []semanticwrite.Document{{Hash: "hash", Text: "relationship"}},
		Fence: semanticwrite.Fence{
			Model: "model", Dimensions: 2, EmbeddingContractID: "contract",
			SearchGenerationID: "generation", SearchGenerationVersion: 1,
		},
		Timeout: time.Second,
	}

	_, err := semanticwrite.NewExecutor(adapter).Execute(context.Background(), plan)

	require.ErrorIs(t, err, semanticwrite.ErrProviderResponseInvalid)
	require.NotErrorIs(t, err, semanticwrite.ErrProviderUnavailable)
}

func TestSearchRepairBatchProviderPreservesTimeoutClassification(t *testing.T) {
	provider := semanticWriteEmbeddingProviderStub{err: &embedding.ProviderHTTPError{Status: 408}}
	adapter := searchRepairBatchProvider{provider: provider}

	_, _, err := adapter.EmbedBatch(context.Background(), []string{"relationship"})

	require.ErrorIs(t, err, semanticwrite.ErrProviderTimeout)
	require.NotErrorIs(t, err, semanticwrite.ErrProviderUnavailable)
}
