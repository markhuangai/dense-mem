package dto

import "time"

// RecallRequest represents query parameters for GET /ui/api/recall.
// The query parameter is the natural-language query; limit caps returned hits.
type RecallRequest struct {
	Query                      string     `query:"query" validate:"required,max=512"`
	Limit                      int        `query:"limit" validate:"min=0,max=50"`
	RelationshipLimit          *int       `query:"relationship_limit" validate:"omitempty,min=0,max=20"`
	CommunityLimit             *int       `query:"community_limit" validate:"omitempty,min=0,max=10"`
	CommunityRelationshipLimit *int       `query:"community_relationship_limit" validate:"omitempty,min=1,max=20"`
	ValidAt                    *time.Time `query:"valid_at"`
	KnownAt                    *time.Time `query:"known_at"`
	KnownEvidenceIDs           []string   `query:"known_evidence_ids" validate:"max=200,dive,uuid"`
	ExpandFromEntityIDs        []string   `query:"expand_from_entity_ids" validate:"max=50,dive,uuid"`
	KnownRelationshipIDs       []string   `query:"known_relationship_ids" validate:"max=200,dive,uuid"`
}
