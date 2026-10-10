package evalharness

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCommunityTopicScoringRejectsLostAndUnrelatedFacts(t *testing.T) {
	judgments := CommunityTopicCohort()
	actual := map[string][]int{}
	for _, topic := range judgments {
		actual[topic.Key] = append([]int(nil), topic.SourceIndices...)
	}
	score := ScoreCommunityTopics(judgments, actual)
	require.Equal(t, 1.0, score.SourcePreservation)
	require.Equal(t, 1.0, score.DistinctFactCoverage)
	require.Zero(t, score.BadAtK)
	require.Zero(t, score.FalseConsolidations)
	actual["runtime"] = append(actual["runtime"][1:], 25)
	score = ScoreCommunityTopics(judgments, actual)
	require.Less(t, score.SourcePreservation, 1.0)
	require.Equal(t, 1, score.BadAtK)
	require.Equal(t, 1, score.FalseConsolidations)
}
