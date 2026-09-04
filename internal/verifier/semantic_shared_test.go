package verifier

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticSharedSpanAndChoiceHelpers(t *testing.T) {
	require.Equal(t, "works", mustSemanticSpan(t, "Mark works.", 5, 10))
	_, err := SemanticEvidenceSpan("short", 3, 2)
	require.Error(t, err)
	_, err = SemanticEvidenceSpan("short", 0, 20)
	require.Error(t, err)
	require.Equal(t, "A B", mustSemanticExactQuote(t, "A B", 0, 3, " A   B "))
	_, err = semanticExactSpanQuote("A B", 0, 3, "different")
	require.Error(t, err)
	require.True(t, semanticWhitespaceEquivalent("A\nB", " A B "))
	require.True(t, semanticOneOf("yes", "no", "yes"))
	require.False(t, semanticOneOf("maybe", "no", "yes"))
}

func mustSemanticSpan(t *testing.T, content string, start, end int) string {
	t.Helper()
	value, err := SemanticEvidenceSpan(content, start, end)
	require.NoError(t, err)
	return value
}

func mustSemanticExactQuote(t *testing.T, content string, start, end int, quote string) string {
	t.Helper()
	value, err := semanticExactSpanQuote(content, start, end, quote)
	require.NoError(t, err)
	return value
}
