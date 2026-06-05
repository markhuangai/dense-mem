package domain

import "time"

// FactStatus represents the lifecycle state of a promoted fact.
type FactStatus string

const (
	// FactStatusActive is a live, authoritative fact.
	FactStatusActive FactStatus = "active"
	// FactStatusRetracted is a fact that has been withdrawn from current
	// knowledge without implying the statement became false.
	FactStatusRetracted FactStatus = "retracted"
	// FactStatusSuperseded is a fact replaced by a newer promoted fact.
	FactStatusSuperseded FactStatus = "superseded"
	// FactStatusNeedsRevalidation is a fact whose remaining support no longer
	// satisfies the public confidence contract.
	FactStatusNeedsRevalidation FactStatus = "needs_revalidation"
)

// IsValid reports whether s is a recognised FactStatus value.
func (s FactStatus) IsValid() bool {
	switch s {
	case FactStatusActive, FactStatusRetracted, FactStatusSuperseded, FactStatusNeedsRevalidation:
		return true
	}
	return false
}

// Fact is the full runtime domain model for a promoted knowledge fact.
//
// A Fact is created by promoting a validated Claim through the knowledge
// pipeline. Once promoted, a Fact is the authoritative statement of
// knowledge within a profile's graph.
//
// Invariant: every Fact MUST carry its ProfileID so that all downstream
// repository and graph queries can enforce profile-level isolation. Never
// omit or zero-out ProfileID when constructing a Fact.
type Fact struct {
	// Identity / scope
	FactID                string `json:"fact_id"`
	ProfileID             string `json:"team_id"`
	OwnerProfileID        string `json:"owner_profile_id,omitempty"`
	OwnerProfileName      string `json:"owner_profile_name,omitempty"`
	CreatedByProfileID    string `json:"created_by_profile_id,omitempty"`
	CreatedByProfileName  string `json:"created_by_profile_name,omitempty"`
	PromotedByProfileID   string `json:"promoted_by_profile_id,omitempty"`
	PromotedByProfileName string `json:"promoted_by_profile_name,omitempty"`

	// Semantic triple (preserved from the promoting Claim)
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`

	// Lifecycle
	Status FactStatus `json:"status"`
	// AuthorityState describes read-time authority after overlay/conflict checks.
	AuthorityState string `json:"authority_state,omitempty"`

	// Quality signal: a [0,1] score reflecting aggregate evidence weight.
	TruthScore float64 `json:"truth_score"`

	// Temporal validity
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidTo         *time.Time `json:"valid_to,omitempty"`
	RecordedAt      time.Time  `json:"recorded_at"`
	RecordedTo      *time.Time `json:"recorded_to,omitempty"`
	RetractedAt     *time.Time `json:"retracted_at,omitempty"`
	LastConfirmedAt *time.Time `json:"last_confirmed_at,omitempty"`

	// Provenance: the Claim that was promoted to create this Fact.
	PromotedFromClaimID string `json:"promoted_from_claim_id"`

	// Classification holds arbitrary key-value labels (e.g. topic, domain)
	// propagated from the promoting Claim.
	// Classification must be a structured map, not a string blob.
	Classification               map[string]any `json:"classification,omitempty"`
	ClassificationLatticeVersion string         `json:"classification_lattice_version,omitempty"`

	// SourceQuality is a [0,1] signal inherited from the supporting fragments
	// via the promoting Claim.
	SourceQuality float64 `json:"source_quality"`

	// Labels are free-form string tags attached to the Fact for filtering.
	Labels []string `json:"labels,omitempty"`

	// Metadata holds arbitrary additional data not captured by typed fields.
	Metadata map[string]any `json:"metadata,omitempty"`

	// Evidence exposes the supporting lineage used to ground the fact.
	Evidence []Evidence `json:"evidence,omitempty"`
}
