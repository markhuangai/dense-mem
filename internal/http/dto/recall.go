package dto

import "time"

// RecallRequest represents query parameters for GET /api/v1/recall.
// The query parameter is the natural-language query; limit caps returned hits.
type RecallRequest struct {
	Query                string     `query:"query" validate:"required,max=512"`
	Limit                int        `query:"limit" validate:"min=0,max=50"`
	ValidAt              *time.Time `query:"valid_at"`
	KnownAt              *time.Time `query:"known_at"`
	KnownEvidenceIDs     []string   `query:"known_evidence_ids" validate:"max=200,dive,uuid"`
	ExpandFromEntityIDs  []string   `query:"expand_from_entity_ids" validate:"max=50,dive,uuid"`
	KnownRelationshipIDs []string   `query:"known_relationship_ids" validate:"max=200,dive,uuid"`
}
