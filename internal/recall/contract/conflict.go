package contract

import "time"

// EvidenceConflictPositionRecord is the immutable read model for one exact
// cited occurrence span returned with recall conflicts.
type EvidenceConflictPositionRecord struct {
	ConflictID               string    `json:"conflict_id,omitempty"`
	PositionID               string    `json:"position_id"`
	PositionKey              string    `json:"position_key,omitempty"`
	CanonicalEvidenceID      string    `json:"evidence_id"`
	CanonicalOwnerProfileID  string    `json:"canonical_owner_profile_id,omitempty"`
	OccurrenceID             string    `json:"occurrence_id"`
	OccurrenceOwnerProfileID string    `json:"occurrence_owner_profile_id,omitempty"`
	Quote                    string    `json:"quote"`
	SpanStart                int       `json:"span_start"`
	SpanEnd                  int       `json:"span_end"`
	Authority                string    `json:"authority"`
	Submitted                bool      `json:"submitted"`
	CreatedAt                time.Time `json:"created_at"`
}

type EvidenceConflictEventRecord struct {
	ConflictEventID     string                           `json:"event_id"`
	ConflictID          string                           `json:"conflict_id"`
	Ordinal             int64                            `json:"ordinal"`
	Action              string                           `json:"action"`
	StatusAfter         string                           `json:"status_after"`
	CaseVersion         int                              `json:"case_version"`
	ActorKind           string                           `json:"actor_kind"`
	ActorID             string                           `json:"actor_id,omitempty"`
	Reason              string                           `json:"reason,omitempty"`
	PreferredPositionID string                           `json:"preferred_position_id,omitempty"`
	CitationSnapshot    []EvidenceConflictPositionRecord `json:"citation_snapshot"`
	CreatedAt           time.Time                        `json:"created_at"`
}

type EvidenceConflictCaseRecord struct {
	TeamID              string                           `json:"team_id"`
	ConflictID          string                           `json:"conflict_id"`
	SpaceID             string                           `json:"space_id"`
	SpaceGeneration     int64                            `json:"space_generation"`
	CaseKey             string                           `json:"-"`
	Kind                string                           `json:"kind"`
	Status              string                           `json:"status"`
	Version             int                              `json:"version"`
	PreferredPositionID string                           `json:"preferred_position_id,omitempty"`
	ResolvedAt          *time.Time                       `json:"resolved_at,omitempty"`
	ResolutionReason    string                           `json:"resolution_reason,omitempty"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
	Positions           []EvidenceConflictPositionRecord `json:"positions"`
	Events              []EvidenceConflictEventRecord    `json:"events,omitempty"`
}
