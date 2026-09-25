package postgres

import (
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type relationshipRecallCandidate struct {
	RelationshipID string
	Score          float64
	BestBranchRank int
	SearchState    string
}

func addRecallRelationshipBranch(acc map[string]*relationshipRecallCandidate, hits []SearchHit, knownRelationships map[string]struct{}, weight float64) {
	for i, hit := range hits {
		if hit.SourceKind != "relationship" || hit.SourceID == "" {
			continue
		}
		if _, known := knownRelationships[hit.SourceID]; known {
			continue
		}
		branchRank := i + 1
		candidate := acc[hit.SourceID]
		if candidate == nil {
			candidate = &relationshipRecallCandidate{
				RelationshipID: hit.SourceID,
				BestBranchRank: branchRank,
				SearchState:    hit.SearchState,
			}
			acc[hit.SourceID] = candidate
		}
		candidate.Score += weight / (recallRRFConstant + float64(branchRank))
		if branchRank < candidate.BestBranchRank {
			candidate.BestBranchRank = branchRank
		}
		candidate.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
	}
}

func sortedRecallRelationshipCandidates(acc map[string]*relationshipRecallCandidate) []relationshipRecallCandidate {
	out := make([]relationshipRecallCandidate, 0, len(acc))
	for _, candidate := range acc {
		out = append(out, *candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].BestBranchRank != out[j].BestBranchRank {
			return out[i].BestBranchRank < out[j].BestBranchRank
		}
		return out[i].RelationshipID < out[j].RelationshipID
	})
	return out
}
