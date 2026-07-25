package domain

import (
	"time"

	"github.com/google/uuid"
)

// Relationship constants for preserved knowledge graph.
// These must match the canonical names used by knowledge graph read models.
const (
	SUPPORTED_BY  = "SUPPORTED_BY"
	PROMOTES_TO   = "PROMOTES_TO"
	SUPERSEDED_BY = "SUPERSEDED_BY"
	CONTRADICTS   = "CONTRADICTS"
	SUBJECT       = "SUBJECT"
	OBJECT        = "OBJECT"
)

// SourceFragmentContract represents the preserved knowledge contract for source fragments.
// Field names must match the canonical names in discovery docs.
type SourceFragmentContract struct {
	FragmentID     uuid.UUID      `json:"fragment_id"`
	Connector      string         `json:"connector"`
	SourceID       string         `json:"source_id"`
	Content        string         `json:"content"`
	Embedding      []float32      `json:"embedding"`
	Classification map[string]any `json:"classification"`
}

// ClaimContract represents the preserved knowledge contract for claims.
// Field names must match the canonical names in discovery docs.
type ClaimContract struct {
	ClaimID           uuid.UUID `json:"claim_id"`
	Predicate         string    `json:"predicate"`
	Modality          string    `json:"modality"`
	Status            string    `json:"status"`
	EntailmentVerdict string    `json:"entailment_verdict"`
	ExtractConf       float64   `json:"extract_conf"`
}

// FactContract represents the preserved knowledge contract for facts.
// Field names must match the canonical names in discovery docs.
type FactContract struct {
	FactID     uuid.UUID  `json:"fact_id"`
	Status     string     `json:"status"`
	TruthScore float64    `json:"truth_score"`
	ValidFrom  *time.Time `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to"`
	RecordedAt time.Time  `json:"recorded_at"`
	RecordedTo *time.Time `json:"recorded_to"`
}
