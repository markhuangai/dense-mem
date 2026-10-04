package evalharness

import "fmt"

type OrganizationItem struct {
	ID        string   `json:"id"`
	SourceIDs []string `json:"source_ids"`
}

type OrganizationScore struct {
	RepeatedSlots        int     `json:"repeated_slots"`
	FalseConsolidations  int     `json:"false_consolidations"`
	DistinctFacts        int     `json:"distinct_facts"`
	RetainedFacts        int     `json:"retained_facts"`
	OriginalSources      int     `json:"original_sources"`
	RetainedSources      int     `json:"retained_sources"`
	DistinctFactCoverage float64 `json:"distinct_fact_coverage"`
	SourcePreservation   float64 `json:"source_preservation"`
}

func ScoreOrganization(testCase OrganizationCase, items []OrganizationItem) (OrganizationScore, error) {
	var score OrganizationScore
	known := map[string]OrganizationSource{}
	facts := map[string]bool{}
	for _, source := range testCase.Sources {
		if source.ID == "" || source.EquivalenceKey == "" || len(source.FactIDs) == 0 {
			return score, fmt.Errorf("invalid organization judgment")
		}
		if _, exists := known[source.ID]; exists {
			return score, fmt.Errorf("duplicate organization source")
		}
		known[source.ID] = source
		for _, fact := range source.FactIDs {
			if fact == "" {
				return score, fmt.Errorf("empty organization fact")
			}
			facts[fact] = true
		}
	}
	if len(known) == 0 {
		return score, fmt.Errorf("organization case has no sources")
	}
	seenItems, seenMeanings, retainedSources, retainedFacts := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range items {
		if item.ID == "" || seenItems[item.ID] || len(item.SourceIDs) == 0 {
			return score, fmt.Errorf("invalid organization result item")
		}
		seenItems[item.ID] = true
		meanings := map[string]bool{}
		for _, sourceID := range item.SourceIDs {
			source, exists := known[sourceID]
			if !exists {
				return score, fmt.Errorf("unknown organization source reference")
			}
			retainedSources[sourceID] = true
			meanings[source.EquivalenceKey] = true
			for _, fact := range source.FactIDs {
				retainedFacts[fact] = true
			}
		}
		if len(meanings) > 1 {
			score.FalseConsolidations++
		}
		if len(meanings) == 1 {
			for meaning := range meanings {
				if seenMeanings[meaning] {
					score.RepeatedSlots++
				}
			}
		}
		for meaning := range meanings {
			seenMeanings[meaning] = true
		}
	}
	score.DistinctFacts = len(facts)
	score.RetainedFacts = len(retainedFacts)
	score.OriginalSources = len(known)
	score.RetainedSources = len(retainedSources)
	score.DistinctFactCoverage = float64(score.RetainedFacts) / float64(score.DistinctFacts)
	score.SourcePreservation = float64(score.RetainedSources) / float64(score.OriginalSources)
	return score, nil
}

func UnorganizedItems(testCase OrganizationCase) []OrganizationItem {
	items := make([]OrganizationItem, 0, len(testCase.Sources))
	for _, source := range testCase.Sources {
		items = append(items, OrganizationItem{ID: source.ID, SourceIDs: []string{source.ID}})
	}
	return items
}
