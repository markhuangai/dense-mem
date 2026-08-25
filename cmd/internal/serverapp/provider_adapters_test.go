package serverapp

import (
	"testing"

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
