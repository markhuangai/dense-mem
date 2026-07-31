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
)

// IsValid reports whether s is a supported DreamStatus value.
func (s DreamStatus) IsValid() bool {
	switch s {
	case DreamStatusProposed, DreamStatusReinforced, DreamStatusStale,
		DreamStatusRejected, DreamStatusSubmitted:
		return true
	default:
		return false
	}
}

// DreamSourceRef identifies one team-visible Relationship source used to
// produce a dream. Type is relationship or candidate_relationship.
type DreamSourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Dream is a generated, reviewable hypothesis. It is not evidence and must not
// be treated as a fact or claim until human feedback routes it through the
// normal memory pipeline.
type Dream struct {
	DreamID                        string           `json:"dream_id"`
	TeamID                         string           `json:"team_id"`
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
	CycleRunID                     string           `json:"cycle_run_id,omitempty"`
	GeneratorModel                 string           `json:"generator_model,omitempty"`
	ContentHash                    string           `json:"content_hash,omitempty"`
	SourceRefs                     []DreamSourceRef `json:"source_refs,omitempty"`
	InvalidatedReason              string           `json:"invalidated_reason,omitempty"`
	CreatedAt                      time.Time        `json:"created_at"`
	UpdatedAt                      time.Time        `json:"updated_at"`
}

// DreamingConfigSettings is the editable global runtime configuration for the
// scheduled team dreaming cycle.
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
	Enabled        bool   `json:"enabled"`
	ForceEnabled   bool   `json:"force_enabled"`
	StartTimeLocal string `json:"start_time_local"`
	Timezone       string `json:"timezone"`
	MaxOutputs     int    `json:"max_outputs"`
}
