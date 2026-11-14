package contract

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitTraceUsesOneSharedEventBudget(t *testing.T) {
	result := &EvidenceTraceResult{Omissions: []string{}}
	remaining := 3
	require.Len(t, BoundEvents([]int{1, 2}, &remaining, result, "occurrences"), 2)
	require.Len(t, BoundEvents([]int{3, 4}, &remaining, result, "source_revisions"), 1)
	require.Empty(t, BoundEvents([]int{5}, &remaining, result, "decisions"))
	require.Zero(t, remaining)
	require.True(t, result.Truncated)
	require.Equal(t, []string{"source_revisions_max_events", "decisions_max_events"}, result.Omissions)
}

func TestEvidenceOnlyUnitTraceDefaultAndMaximumLimits(t *testing.T) {
	input := NormalizeInput(Input{})
	require.Equal(t, 100, input.MaxEvents)
	require.Equal(t, 2000, input.MaxFragmentContentRunes)
	input = NormalizeInput(Input{MaxEvents: 1000, MaxFragmentContentRunes: 20000})
	require.Equal(t, 500, input.MaxEvents)
	require.Equal(t, 16384, input.MaxFragmentContentRunes)
}
