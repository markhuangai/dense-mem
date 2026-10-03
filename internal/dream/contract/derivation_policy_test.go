package contract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGraphSourceAndDerivationPolicy(t *testing.T) {
	for _, status := range []string{"active", "pending_evidence"} {
		require.True(t, GraphSourceStatusEligible(status))
	}
	for _, status := range []string{"", "stale", "rejected", "submitted"} {
		require.False(t, GraphSourceStatusEligible(status))
	}
	input := validPolicyGraphProposal()
	require.True(t, GraphSourceValidationRequired(input))
	require.True(t, GraphSourceCountValid(input))
	require.True(t, GraphDerivationMatchesSourceVersion(input.Derivations[0], input.SourceVersions))
	require.True(t, GraphDerivationsCoverSources(input.Derivations, input.SourceVersions))
	changed := input.Derivations[0]
	changed.RelationshipVersion++
	require.False(t, GraphDerivationMatchesSourceVersion(changed, input.SourceVersions))
	require.False(t, GraphDerivationsCoverSources(input.Derivations[:1], input.SourceVersions))
	input.GeneratorKind = "evaluation_seed"
	require.False(t, GraphSourceValidationRequired(input))
	input.SourceVersions = nil
	require.False(t, GraphSourceCountValid(input))

	derivation := validPolicyGraphProposal().Derivations[0]
	excerpt := DreamEvidence{
		SupportID: derivation.SupportID, FragmentID: derivation.FragmentID,
		SourceGroupKey: derivation.SourceGroupKey, SpanStart: derivation.SpanStart,
		SpanEnd: derivation.SpanEnd, Content: derivation.Quote, Authority: derivation.Authority,
	}
	require.True(t, GraphDerivationMatchesEvidence(derivation, []DreamEvidence{excerpt}))
	excerpt.Content = "B"
	require.False(t, GraphDerivationMatchesEvidence(derivation, []DreamEvidence{excerpt}))
}
