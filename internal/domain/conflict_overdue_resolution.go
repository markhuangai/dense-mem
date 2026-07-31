package domain

import (
	"sort"
	"strings"
	"time"
)

const (
	ConflictAssessmentDecisionSelect  = "select"
	ConflictAssessmentDecisionAbstain = "abstain"

	ConflictResolutionReasonAI  = "overdue_ai_assessment"
	ConflictResolutionReasonLWW = "overdue_last_write_wins"
)

// ConflictResolutionSupport is the server-owned comparison input for an
// overdue conflict. AcceptedAt is when Dense-Mem accepted the support, not a
// model-provided timestamp.
type ConflictResolutionSupport struct {
	Authority  string
	AcceptedAt time.Time
}

// ConflictResolutionPosition is one mutually incompatible position in an
// overdue conflict.
type ConflictResolutionPosition struct {
	PositionID string
	Supports   []ConflictResolutionSupport
}

// ConflictLastWriteWinner is the deterministic fallback selected after an AI
// abstention or five failed daily assessments for the same case version.
type ConflictLastWriteWinner struct {
	PositionID string
	Authority  string
	AcceptedAt time.Time
}

// SelectConflictLastWriteWinner first prefers the strongest source authority,
// then the latest Dense-Mem accepted support at that authority. Position ID is
// only a stable tie-breaker.
func SelectConflictLastWriteWinner(positions []ConflictResolutionPosition) (ConflictLastWriteWinner, bool) {
	candidates := make([]ConflictLastWriteWinner, 0, len(positions))
	for _, position := range positions {
		positionID := strings.TrimSpace(position.PositionID)
		if positionID == "" {
			continue
		}
		bestRank := 0
		bestAuthority := ""
		bestAcceptedAt := time.Time{}
		for _, support := range position.Supports {
			rank, authority := conflictAuthorityRank(support.Authority)
			if bestAuthority == "" || rank < bestRank || (rank == bestRank && support.AcceptedAt.After(bestAcceptedAt)) {
				bestRank = rank
				bestAuthority = authority
				bestAcceptedAt = support.AcceptedAt.UTC()
			}
		}
		if bestAuthority == "" {
			continue
		}
		candidates = append(candidates, ConflictLastWriteWinner{
			PositionID: positionID,
			Authority:  bestAuthority,
			AcceptedAt: bestAcceptedAt,
		})
	}
	if len(candidates) == 0 {
		return ConflictLastWriteWinner{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftRank, _ := conflictAuthorityRank(candidates[i].Authority)
		rightRank, _ := conflictAuthorityRank(candidates[j].Authority)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !candidates[i].AcceptedAt.Equal(candidates[j].AcceptedAt) {
			return candidates[i].AcceptedAt.After(candidates[j].AcceptedAt)
		}
		return candidates[i].PositionID < candidates[j].PositionID
	})
	return candidates[0], true
}

func conflictAuthorityRank(authority string) (int, string) {
	switch strings.ToLower(strings.TrimSpace(authority)) {
	case "authoritative":
		return 0, "authoritative"
	case "primary":
		return 1, "primary"
	case "secondary":
		return 2, "secondary"
	case "inferred", "derived":
		return 3, "inferred"
	default:
		return 4, "unknown"
	}
}
