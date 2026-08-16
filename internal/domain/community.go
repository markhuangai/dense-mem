package domain

import "time"

// Community is a persisted, team-scoped summary of a detected graph community.
type Community struct {
	CommunityID      string    `json:"community_id"`
	TeamID           string    `json:"team_id"`
	Level            int       `json:"level"`
	Summary          string    `json:"summary"`
	SummaryVersion   string    `json:"summary_version"`
	MemberCount      int       `json:"member_count"`
	TopEntities      []string  `json:"top_entities,omitempty"`
	TopPredicates    []string  `json:"top_predicates,omitempty"`
	LastSummarizedAt time.Time `json:"last_summarized_at"`
}

// CommunityDetectionConfigSettings is the editable global runtime configuration
// for scheduled community detection.
type CommunityDetectionConfigSettings struct {
	UpdateTime string                          `json:"update_time"`
	Items      []CommunityDetectionConfigItem  `json:"items"`
	Effective  CommunityDetectionRuntimeConfig `json:"effective"`
}

// CommunityDetectionConfigItem is one control-panel editable config entry.
type CommunityDetectionConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CommunityDetectionRuntimeConfig is the effective global default for the
// scheduled detector.
type CommunityDetectionRuntimeConfig struct {
	Enabled        bool   `json:"enabled"`
	StartTimeLocal string `json:"start_time_local"`
	Timezone       string `json:"timezone"`
	MaxConcurrency int    `json:"max_concurrency"`
	JitterSeconds  int    `json:"jitter_seconds"`
}
