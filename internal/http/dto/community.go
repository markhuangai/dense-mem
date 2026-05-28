package dto

import "time"

// CommunityDetectRequest is the request body for triggering community
// detection on a profile's knowledge graph.
//
// All fields are optional tuning parameters. When omitted the service uses its
// built-in defaults.
type CommunityDetectRequest struct {
	// Gamma controls the resolution parameter for Leiden community detection.
	// Higher values produce more, smaller communities. Defaults to 1.0 when zero.
	Gamma float64 `json:"gamma,omitempty" validate:"omitempty,gte=0"`
	// Tolerance is the convergence threshold for iterative algorithms. Smaller
	// values increase precision at the cost of more iterations.
	Tolerance float64 `json:"tolerance,omitempty" validate:"omitempty,gte=0"`
	// MaxLevels caps the number of hierarchical community-merge levels.
	// Defaults to 10 when zero.
	MaxLevels int `json:"max_levels,omitempty" validate:"omitempty,gte=1"`
}

// CommunityResponse is the public representation of a persisted community summary.
type CommunityResponse struct {
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

// ListCommunitiesRequest represents query parameters for listing communities.
type ListCommunitiesRequest struct {
	Limit int `query:"limit" validate:"min=0,max=100"`
}

// ListCommunitiesResponse is the list envelope for community summaries.
type ListCommunitiesResponse struct {
	Items []CommunityResponse `json:"items"`
	Total int                 `json:"total"`
}

// CommunityDetectResponse is the success payload returned after detection runs.
type CommunityDetectResponse struct {
	Detected       bool                `json:"detected"`
	CommunityCount int                 `json:"community_count"`
	NodeCount      int                 `json:"node_count"`
	Communities    []CommunityResponse `json:"communities,omitempty"`
}
