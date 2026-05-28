package domain

import "time"

// Community is a persisted, profile-scoped summary of a detected graph community.
type Community struct {
	CommunityID      string    `json:"community_id"`
	ProfileID        string    `json:"team_id"`
	Level            int       `json:"level"`
	Summary          string    `json:"summary"`
	SummaryVersion   string    `json:"summary_version"`
	MemberCount      int       `json:"member_count"`
	TopEntities      []string  `json:"top_entities,omitempty"`
	TopPredicates    []string  `json:"top_predicates,omitempty"`
	LastSummarizedAt time.Time `json:"last_summarized_at"`
}
