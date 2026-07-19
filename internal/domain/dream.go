package domain

import "time"

// DreamStatus is the lifecycle state for a generated hypothesis.
type DreamStatus string

const (
	DreamStatusProposed   DreamStatus = "proposed"
	DreamStatusReinforced DreamStatus = "reinforced"
	DreamStatusStale      DreamStatus = "stale"
	DreamStatusRejected   DreamStatus = "rejected"
	DreamStatusSubmitted  DreamStatus = "submitted"
	// Deprecated: use DreamStatusSubmitted. Hypotheses are submitted to remember;
	// they are never promoted directly into semantic truth.
	DreamStatusPromoted DreamStatus = "promoted"
)

// IsValid reports whether s is a supported DreamStatus value.
func (s DreamStatus) IsValid() bool {
	switch s {
	case DreamStatusProposed, DreamStatusReinforced, DreamStatusStale,
		DreamStatusRejected, DreamStatusSubmitted, DreamStatusPromoted:
		return true
	default:
		return false
	}
}

// DreamSourceRef identifies one same-profile source used to produce or
// evaluate a dream. Type is one of fact, claim, fragment, community, or dream.
type DreamSourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Dream is a generated, reviewable hypothesis. It is not evidence and must not
// be treated as a fact or claim until human feedback routes it through the
// normal memory pipeline.
type Dream struct {
	DreamID                        string           `json:"dream_id"`
	ProfileID                      string           `json:"team_id"`
	Hypothesis                     string           `json:"hypothesis"`
	WhatIf                         string           `json:"what_if"`
	PossibleOutcome                string           `json:"possible_outcome"`
	Rationale                      string           `json:"rationale"`
	Likelihood                     float64          `json:"likelihood"`
	Confidence                     float64          `json:"confidence"`
	SourceOwnerProfileIDs          []string         `json:"source_owner_profile_ids,omitempty"`
	SubjectEntityID                string           `json:"subject_entity_id,omitempty"`
	PredicateKey                   string           `json:"predicate_key,omitempty"`
	ObjectEntityID                 string           `json:"object_entity_id,omitempty"`
	ObjectValueID                  string           `json:"object_value_id,omitempty"`
	SourceRelationshipIDs          []string         `json:"source_relationship_ids,omitempty"`
	SourceCandidateRelationshipIDs []string         `json:"source_candidate_relationship_ids,omitempty"`
	SourceVersions                 map[string]int   `json:"source_versions,omitempty"`
	GeneratorKind                  string           `json:"generator_kind,omitempty"`
	GeneratorVersion               string           `json:"generator_version,omitempty"`
	Status                         DreamStatus      `json:"status"`
	Cycle                          string           `json:"cycle"`
	CycleRunID                     string           `json:"cycle_run_id,omitempty"`
	GeneratorModel                 string           `json:"generator_model,omitempty"`
	ContentHash                    string           `json:"content_hash,omitempty"`
	SourceRefs                     []DreamSourceRef `json:"source_refs,omitempty"`
	LastEvaluatedAt                *time.Time       `json:"last_evaluated_at,omitempty"`
	InvalidatedReason              string           `json:"invalidated_reason,omitempty"`
	CreatedAt                      time.Time        `json:"created_at"`
	UpdatedAt                      time.Time        `json:"updated_at"`
}

// DreamingConfigSettings is the editable global runtime configuration for the
// nightly reflect -> re-evaluate -> dream cycle chain.
type DreamingConfigSettings struct {
	UpdateTime string                `json:"update_time"`
	Items      []DreamingConfigItem  `json:"items"`
	Effective  DreamingRuntimeConfig `json:"effective"`
}

// DreamingConfigItem is one control-panel editable config entry.
type DreamingConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DreamingRuntimeConfig is the effective global default. Per-team config may
// override these values unless ForceEnabled is true.
type DreamingRuntimeConfig struct {
	Enabled           bool   `json:"enabled"`
	ForceEnabled      bool   `json:"force_enabled"`
	StartTimeLocal    string `json:"start_time_local"`
	Timezone          string `json:"timezone"`
	ReflectEnabled    bool   `json:"reflect_enabled"`
	ReevaluateEnabled bool   `json:"reevaluate_enabled"`
	DreamEnabled      bool   `json:"dream_enabled"`
	MaxOutputs        int    `json:"max_outputs"`
}
