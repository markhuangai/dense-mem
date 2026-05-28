package dto

import "time"

// FactResponse represents a promoted fact in API responses.
type FactResponse struct {
	FactID                       string         `json:"fact_id"`
	ProfileID                    string         `json:"team_id"`
	CreatedByProfileID           string         `json:"created_by_profile_id,omitempty"`
	CreatedByProfileName         string         `json:"created_by_profile_name,omitempty"`
	PromotedByProfileID          string         `json:"promoted_by_profile_id,omitempty"`
	PromotedByProfileName        string         `json:"promoted_by_profile_name,omitempty"`
	Subject                      string         `json:"subject"`
	Predicate                    string         `json:"predicate"`
	Object                       string         `json:"object"`
	Status                       string         `json:"status"`
	TruthScore                   float64        `json:"truth_score"`
	ValidFrom                    *time.Time     `json:"valid_from,omitempty"`
	ValidTo                      *time.Time     `json:"valid_to,omitempty"`
	RecordedAt                   time.Time      `json:"recorded_at"`
	RecordedTo                   *time.Time     `json:"recorded_to,omitempty"`
	RetractedAt                  *time.Time     `json:"retracted_at,omitempty"`
	LastConfirmedAt              *time.Time     `json:"last_confirmed_at,omitempty"`
	PromotedFromClaimID          string         `json:"promoted_from_claim_id"`
	Classification               map[string]any `json:"classification,omitempty"`
	ClassificationLatticeVersion string         `json:"classification_lattice_version,omitempty"`
	SourceQuality                float64        `json:"source_quality"`
	Labels                       []string       `json:"labels,omitempty"`
	Metadata                     map[string]any `json:"metadata,omitempty"`
	Evidence                     []Evidence     `json:"evidence,omitempty"`
}

// ListFactsRequest represents query parameters for listing facts.
// Validation rules:
//   - Limit: optional, 0-100
//   - Cursor: optional, max 256 characters
//   - Subject: optional, max 256 characters
//   - Predicate: optional, max 128 characters
//   - Status: optional, oneof active retracted superseded needs_revalidation
type ListFactsRequest struct {
	Limit           int        `query:"limit" validate:"min=0,max=100"`
	Cursor          string     `query:"cursor" validate:"max=256"`
	Subject         string     `query:"subject" validate:"max=256"`
	Predicate       string     `query:"predicate" validate:"max=128"`
	Status          string     `query:"status" validate:"omitempty,oneof=active retracted superseded needs_revalidation"`
	ValidAt         *time.Time `query:"valid_at"`
	KnownAt         *time.Time `query:"known_at"`
	IncludeEvidence bool       `query:"include_evidence"`
}

// ListFactsResponse represents a paginated list of facts.
type ListFactsResponse struct {
	Items      []FactResponse `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
}
