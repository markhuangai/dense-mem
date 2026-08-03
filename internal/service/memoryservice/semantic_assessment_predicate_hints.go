package memoryservice

import "strings"

func clientProposedPredicateKeys(proposal map[string]any) []string {
	seen := map[string]struct{}{}
	keys := make([]string, 0)
	for _, relationship := range placementReviewObjectArray(proposal, "relationship_hints", "relationships") {
		predicate, ok := reviewMap(relationship["predicate"])
		if !ok {
			continue
		}
		key := strings.TrimSpace(reviewString(predicate, "proposed_key"))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}
