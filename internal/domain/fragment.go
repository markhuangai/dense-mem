package domain

import (
	"time"
)

// FragmentStatus represents the lifecycle state of a Fragment.
type FragmentStatus string

const (
	// FragmentStatusActive is the default state for a fragment that is live and searchable.
	FragmentStatusActive FragmentStatus = "active"
	// FragmentStatusRetracted marks a fragment that has been withdrawn but not hard-deleted.
	FragmentStatusRetracted FragmentStatus = "retracted"
)

// SourceType represents the origin type of a Fragment.
type SourceType string

const (
	SourceTypeConversation SourceType = "conversation"
	SourceTypeDocument     SourceType = "document"
	SourceTypeObservation  SourceType = "observation"
	SourceTypeManual       SourceType = "manual"
)

// Fragment represents a knowledge fragment stored in the system.
// The FragmentID field uses Go name FragmentID but JSON tag "id" so the public API surface
// reads as "id": "..." (AC-41 compatibility clause) while internal code references FragmentID
// to match the Neo4j constraint name sourcefragment_fragment_id_unique.
type Fragment struct {
	FragmentID           string         `json:"id"` // API field "id" maps to stored fragment_id
	ProfileID            string         `json:"team_id"`
	OwnerProfileID       string         `json:"owner_profile_id,omitempty"`
	OwnerProfileName     string         `json:"owner_profile_name,omitempty"`
	CreatedByProfileID   string         `json:"created_by_profile_id,omitempty"`
	CreatedByProfileName string         `json:"created_by_profile_name,omitempty"`
	Content              string         `json:"content"`
	Source               string         `json:"source,omitempty"`
	SourceType           SourceType     `json:"source_type"`
	Authority            Authority      `json:"authority,omitempty"`
	Labels               []string       `json:"labels,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
	ContentHash          string         `json:"content_hash"`
	IdempotencyKey       string         `json:"idempotency_key,omitempty"`
	EmbeddingModel       string         `json:"embedding_model"`
	EmbeddingDimensions  int            `json:"embedding_dimensions"`
	// SourceQuality is a [0,1] signal used by downstream claim extraction to weight
	// evidence reliability. Claims inherit this value from their supporting fragments.
	SourceQuality float64 `json:"source_quality"`
	// Classification holds arbitrary key-value labels (e.g. topic, sentiment) produced
	// during ingestion. Claims propagate these labels for upstream filtering.
	Classification map[string]any `json:"classification,omitempty"`
	// Status tracks the lifecycle state of the fragment (active or retracted).
	// Defaults to active on creation. Hard-delete behavior is handled separately.
	Status FragmentStatus `json:"status,omitempty"`
	// RecordedTo is retained for backward-compatible responses on legacy rows.
	// New retractions use RetractedAt.
	RecordedTo  *time.Time `json:"recorded_to,omitempty"`
	RetractedAt *time.Time `json:"retracted_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// Embedding vector deliberately NOT included in default read response (AC-28).
}

// ValidSourceTypes returns all valid SourceType values.
func ValidSourceTypes() []SourceType {
	return []SourceType{
		SourceTypeConversation,
		SourceTypeDocument,
		SourceTypeObservation,
		SourceTypeManual,
	}
}

// IsValid returns true if the SourceType is a valid value.
func (s SourceType) IsValid() bool {
	switch s {
	case SourceTypeConversation, SourceTypeDocument, SourceTypeObservation, SourceTypeManual:
		return true
	default:
		return false
	}
}
