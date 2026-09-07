// Package contract contains recall's caller-owned request boundary.
package contract

import "time"

type Request struct {
	Query                      string     `json:"query"`
	Limit                      int        `json:"limit,omitempty"`
	IncludeHypotheses          bool       `json:"-"`
	RelationshipLimit          *int       `json:"relationship_limit,omitempty"`
	CommunityLimit             *int       `json:"community_limit,omitempty"`
	CommunityRelationshipLimit *int       `json:"community_relationship_limit,omitempty"`
	ValidAt                    *time.Time `json:"valid_at,omitempty"`
	KnownAt                    *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs           []string   `json:"known_evidence_ids,omitempty"`
	KnownRelationshipIDs       []string   `json:"known_relationship_ids,omitempty"`
	ExpandFromEntityIDs        []string   `json:"expand_from_entity_ids,omitempty"`
}

type RecallRequest = Request
