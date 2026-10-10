package evalharness

type CommunityTopicJudgment struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	SourceIndices []int  `json:"source_indices"`
}

func CommunityTopicCohort() []CommunityTopicJudgment {
	return []CommunityTopicJudgment{
		{Key: "runtime", Label: "Runtime tools", SourceIndices: []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{Key: "storage", Label: "Storage tools", SourceIndices: []int{9, 10, 11, 12, 13, 14, 15, 16, 17}},
		{Key: "maintenance", Label: "Maintenance tools", SourceIndices: []int{18, 19, 20, 21, 22, 23, 24, 25}},
	}
}

type CommunityTopicScore struct {
	SourcePreservation   float64 `json:"source_preservation"`
	DistinctFactCoverage float64 `json:"distinct_fact_coverage"`
	BadAtK               int     `json:"bad_at_k"`
	FalseConsolidations  int     `json:"false_consolidations"`
}

func ScoreCommunityTopics(judgments []CommunityTopicJudgment, actual map[string][]int) CommunityTopicScore {
	expected := map[string]map[int]bool{}
	total, retained := 0, 0
	for _, topic := range judgments {
		expected[topic.Key] = map[int]bool{}
		for _, source := range topic.SourceIndices {
			expected[topic.Key][source] = true
			total++
		}
	}
	score := CommunityTopicScore{}
	for key, sources := range actual {
		seen := map[int]bool{}
		for _, source := range sources {
			if !expected[key][source] {
				score.BadAtK++
				continue
			}
			if !seen[source] {
				retained++
				seen[source] = true
			}
		}
		if len(seen) < len(expected[key]) {
			score.FalseConsolidations += len(expected[key]) - len(seen)
		}
	}
	for key, sources := range expected {
		if _, ok := actual[key]; !ok {
			score.FalseConsolidations += len(sources)
		}
	}
	if total > 0 {
		score.SourcePreservation = float64(retained) / float64(total)
		score.DistinctFactCoverage = score.SourcePreservation
	}
	return score
}
