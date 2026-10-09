package recall

import (
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

const maxEquivalentEvidenceIDs = 20

func organizationDegradation(frontier, code string) []RecallDegradationResult {
	if code == "" {
		return nil
	}
	message := "ontology organization was unavailable; ordinary eligible retrieval was used"
	switch code {
	case "ontology_temporal_not_supported":
		message = "ontology organization is current-only and was omitted for temporal recall"
	case "ontology_stale":
		message = "stale ontology organization was omitted; ordinary eligible retrieval remains available"
	case "ontology_bound_exceeded":
		message = "ontology organization exceeded its read bound; ordinary eligible retrieval was used"
	case "ontology_group_overlap":
		message = "overlapping ontology groups were omitted; ordinary eligible retrieval was used"
	}
	return []RecallDegradationResult{{Frontier: frontier, Optional: true, Code: code, Message: message}}
}

func eligibleEvidenceGroups(groups []ontology.RecallEvidenceGroup, hits map[string]recallcontract.RecallEvidenceHit, knownIDs []string) (map[string]ontology.RecallEvidenceGroup, map[string]bool, bool) {
	known, membership, overlap := map[string]bool{}, map[string]string{}, map[string]bool{}
	for _, id := range knownIDs {
		if _, eligible := hits[id]; eligible {
			known[id] = true
		}
	}
	for _, group := range groups {
		for _, id := range group.Members {
			if _, eligible := hits[id]; !eligible {
				continue
			}
			if previous, exists := membership[id]; exists && previous != group.ID {
				overlap[previous], overlap[group.ID] = true, true
			}
			membership[id] = group.ID
		}
	}
	byID, suppressed := map[string]ontology.RecallEvidenceGroup{}, map[string]bool{}
	for _, group := range groups {
		if overlap[group.ID] {
			continue
		}
		eligible := ontology.RecallEvidenceGroup{ID: group.ID}
		knownGroup := false
		for _, id := range group.Members {
			if _, ok := hits[id]; ok {
				eligible.Members = append(eligible.Members, id)
				knownGroup = knownGroup || known[id]
			}
		}
		eligible.Members = domain.NormalizeReadIDList(eligible.Members)
		for _, id := range eligible.Members {
			byID[id] = eligible
			if knownGroup {
				suppressed[id] = true
			}
		}
	}
	return byID, suppressed, len(overlap) > 0
}

func selectRecallEvidence(candidates []recallCandidate, hydration *recallcontract.EvidenceHydration, input recallcontract.RecallEvidenceInput) ([]recallcontract.RecallEvidenceHit, []recallcontract.RecallEvidenceHit, string) {
	groups, suppressed, overlap := eligibleEvidenceGroups(hydration.Groups, hydration.Hits, input.KnownEvidenceIDs)
	seenGroups := map[string]bool{}
	results, conflicts := []recallcontract.RecallEvidenceHit{}, []recallcontract.RecallEvidenceHit{}
	for _, candidate := range candidates {
		hit, eligible := hydration.Hits[candidate.ID]
		if !eligible || suppressed[candidate.ID] {
			continue
		}
		group := groups[candidate.ID]
		if group.ID != "" && seenGroups[group.ID] {
			continue
		}
		seenGroups[group.ID] = true
		hit.EquivalentEvidenceIDs = []string{}
		for _, id := range group.Members {
			if id != hit.EvidenceID {
				hit.EquivalentEvidenceIDs = append(hit.EquivalentEvidenceIDs, id)
			}
		}
		sort.Strings(hit.EquivalentEvidenceIDs)
		hit.EquivalentsTruncated = len(hit.EquivalentEvidenceIDs) > maxEquivalentEvidenceIDs
		if hit.EquivalentsTruncated {
			hit.EquivalentEvidenceIDs = hit.EquivalentEvidenceIDs[:maxEquivalentEvidenceIDs]
		}
		for _, id := range hit.EquivalentEvidenceIDs {
			alternate := hydration.Hits[id]
			hit.RelationshipIDs = append(hit.RelationshipIDs, alternate.RelationshipIDs...)
			hit.SearchState = domain.CombineSearchProjectionStates(hit.SearchState, alternate.SearchState)
			conflicts = append(conflicts, alternate)
		}
		hit.RelationshipIDs = domain.NormalizeReadIDList(hit.RelationshipIDs)
		hit.Score, hit.SpaceKind, hit.Rank = candidate.Score, input.SpaceKind, len(results)+1
		hit.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		results = append(results, hit)
		conflicts = append(conflicts, hit)
		if len(results) == input.Limit {
			break
		}
	}
	code := hydration.OrganizationDegradation
	if overlap {
		code = "ontology_group_overlap"
	}
	return results, conflicts, code
}
