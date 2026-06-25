package recallquality

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScoreAtKMeasuresRelevantAndJudgedBadResults(t *testing.T) {
	got := ScoreAtK(
		[]ResultRef{
			{Type: "fragment", ID: "stale"},
			{Type: "fragment", ID: "expected"},
			{Type: "fact", ID: "decision"},
		},
		[]Judgment{
			{Type: "fragment", ID: "expected", Grade: 3},
			{Type: "fact", ID: "decision", Grade: 2},
		},
		[]ResultRef{{Type: "fragment", ID: "stale"}},
		3,
	)

	require.Equal(t, 3, got.K)
	require.Equal(t, 2, got.RelevantAtK)
	require.Equal(t, 2, got.RelevantTotal)
	require.Equal(t, 1, got.BadAtK)
	require.Equal(t, 1.0, got.RecallAtK)
	require.Equal(t, 0.5, got.MRR)
	require.InDelta(t, 0.665, got.NDCGAtK, 0.001)
}

func TestScoreAtKHandlesNoRelevantJudgments(t *testing.T) {
	got := ScoreAtK(
		[]ResultRef{{Type: "fragment", ID: "one"}},
		nil,
		[]ResultRef{{Type: "fragment", ID: "one"}},
		10,
	)

	require.Equal(t, 1, got.K)
	require.Equal(t, 1, got.BadAtK)
	require.Zero(t, got.RelevantAtK)
	require.Zero(t, got.RelevantTotal)
	require.Zero(t, got.RecallAtK)
	require.Zero(t, got.MRR)
	require.Zero(t, got.NDCGAtK)
}

func TestScoreAtKDeduplicatesRelevantGainForNDCG(t *testing.T) {
	got := ScoreAtK(
		[]ResultRef{
			{Type: "fragment", ID: "expected"},
			{Type: "fragment", ID: "expected"},
		},
		[]Judgment{{Type: "fragment", ID: "expected", Grade: 3}},
		nil,
		2,
	)

	require.Equal(t, 1, got.RelevantAtK)
	require.Equal(t, 1.0, got.RecallAtK)
	require.Equal(t, 1.0, got.MRR)
	require.Equal(t, 1.0, got.NDCGAtK)
}
