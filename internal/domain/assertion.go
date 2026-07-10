package domain

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

type AssertionTier string

const (
	AssertionTierCandidate      AssertionTier = "candidate"
	AssertionTierValidatedClaim AssertionTier = "validated_claim"
	AssertionTierFact           AssertionTier = "fact"
	AssertionTierDream          AssertionTier = "dream"
)

func (t AssertionTier) IsValid() bool {
	switch t {
	case AssertionTierCandidate, AssertionTierValidatedClaim, AssertionTierFact, AssertionTierDream:
		return true
	}
	return false
}

type AssertionStatus string

const (
	AssertionStatusActive      AssertionStatus = "active"
	AssertionStatusNeedsReview AssertionStatus = "needs_review"
	AssertionStatusQuarantined AssertionStatus = "quarantined"
	AssertionStatusSuperseded  AssertionStatus = "superseded"
	AssertionStatusDisputed    AssertionStatus = "disputed"
	AssertionStatusRetracted   AssertionStatus = "retracted"
	AssertionStatusRejected    AssertionStatus = "rejected"
)

func (s AssertionStatus) IsValid() bool {
	switch s {
	case AssertionStatusActive, AssertionStatusNeedsReview, AssertionStatusQuarantined,
		AssertionStatusSuperseded, AssertionStatusDisputed, AssertionStatusRetracted,
		AssertionStatusRejected:
		return true
	}
	return false
}

type AssertionPolicyFamily string

const (
	AssertionPolicyEventAppendOnly AssertionPolicyFamily = "event_append_only"
	AssertionPolicyMultiState      AssertionPolicyFamily = "multi_state"
	AssertionPolicySingleState     AssertionPolicyFamily = "single_state"
	AssertionPolicyVersioned       AssertionPolicyFamily = "versioned"
)

func (f AssertionPolicyFamily) IsValid() bool {
	switch f {
	case AssertionPolicyEventAppendOnly, AssertionPolicyMultiState,
		AssertionPolicySingleState, AssertionPolicyVersioned:
		return true
	}
	return false
}

type EntityResolutionStatus string

const (
	EntityResolutionCanonical   EntityResolutionStatus = "canonical"
	EntityResolutionProvisional EntityResolutionStatus = "provisional"
	EntityResolutionAmbiguous   EntityResolutionStatus = "ambiguous"
)

func (s EntityResolutionStatus) IsValid() bool {
	switch s {
	case EntityResolutionCanonical, EntityResolutionProvisional, EntityResolutionAmbiguous:
		return true
	}
	return false
}

type Entity struct {
	EntityID         string                 `json:"entity_id"`
	ProfileID        string                 `json:"team_id"`
	CanonicalName    string                 `json:"canonical_name"`
	NormalizedName   string                 `json:"normalized_name"`
	EntityType       string                 `json:"entity_type"`
	Aliases          []string               `json:"aliases,omitempty"`
	ResolutionStatus EntityResolutionStatus `json:"resolution_status"`
	ResolutionConf   float64                `json:"resolution_conf"`
	Embedding        []float32              `json:"-"`
	EmbeddingModel   string                 `json:"embedding_model,omitempty"`
	FirstSeenAt      time.Time              `json:"first_seen_at"`
	LastSeenAt       time.Time              `json:"last_seen_at"`
}

func (e Entity) Validate() error {
	switch {
	case strings.TrimSpace(e.EntityID) == "":
		return errors.New("entity_id is required")
	case strings.TrimSpace(e.ProfileID) == "":
		return errors.New("team_id is required")
	case strings.TrimSpace(e.CanonicalName) == "":
		return errors.New("canonical_name is required")
	case strings.TrimSpace(e.NormalizedName) == "":
		return errors.New("normalized_name is required")
	case strings.TrimSpace(e.EntityType) == "":
		return errors.New("entity_type is required")
	case !e.ResolutionStatus.IsValid():
		return errors.New("resolution_status is invalid")
	case e.ResolutionConf < 0 || e.ResolutionConf > 1:
		return errors.New("resolution_conf must be between 0 and 1")
	}
	return nil
}

type ValueType string

const (
	ValueTypeString   ValueType = "string"
	ValueTypeNumber   ValueType = "number"
	ValueTypeBoolean  ValueType = "boolean"
	ValueTypeDate     ValueType = "date"
	ValueTypeDateTime ValueType = "date_time"
)

func (t ValueType) IsValid() bool {
	switch t {
	case ValueTypeString, ValueTypeNumber, ValueTypeBoolean, ValueTypeDate, ValueTypeDateTime:
		return true
	}
	return false
}

type TypedValue struct {
	ValueID   string    `json:"value_id"`
	ValueType ValueType `json:"value_type"`
	Value     string    `json:"value"`
	Display   string    `json:"display,omitempty"`
	Unit      string    `json:"unit,omitempty"`
}

func (v TypedValue) Validate() error {
	value := strings.TrimSpace(v.Value)
	switch {
	case strings.TrimSpace(v.ValueID) == "":
		return errors.New("value_id is required")
	case !v.ValueType.IsValid():
		return errors.New("value_type is invalid")
	case value == "":
		return errors.New("value is required")
	}
	switch v.ValueType {
	case ValueTypeNumber:
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("value must be a finite number")
		}
	case ValueTypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return errors.New("value must be a boolean")
		}
	case ValueTypeDate:
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return errors.New("value must be an ISO 8601 date")
		}
	case ValueTypeDateTime:
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return errors.New("value must be an RFC 3339 date-time")
		}
	}
	return nil
}

type EvidenceSpan struct {
	FragmentID  string `json:"fragment_id"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	SourceGroup string `json:"source_group"`
}

func (s EvidenceSpan) Validate() error {
	switch {
	case strings.TrimSpace(s.FragmentID) == "":
		return errors.New("fragment_id is required")
	case s.Start < 0:
		return errors.New("span start must not be negative")
	case s.End <= s.Start:
		return errors.New("span end must be greater than start")
	case strings.TrimSpace(s.SourceGroup) == "":
		return errors.New("source_group is required")
	}
	return nil
}

type Assertion struct {
	AssertionID       string                `json:"assertion_id"`
	ProfileID         string                `json:"team_id"`
	OwnerProfileID    string                `json:"owner_profile_id,omitempty"`
	SubjectEntityID   string                `json:"subject_entity_id"`
	PredicateKey      string                `json:"predicate_key"`
	RelationshipType  string                `json:"relationship_type"`
	ObjectEntityID    string                `json:"object_entity_id,omitempty"`
	ObjectValue       *TypedValue           `json:"object_value,omitempty"`
	Tier              AssertionTier         `json:"tier"`
	Status            AssertionStatus       `json:"status"`
	PolicyFamily      AssertionPolicyFamily `json:"policy_family"`
	Polarity          ClaimPolarity         `json:"polarity"`
	Modality          ClaimModality         `json:"modality"`
	ValidFrom         *time.Time            `json:"valid_from,omitempty"`
	ValidTo           *time.Time            `json:"valid_to,omitempty"`
	RecordedAt        time.Time             `json:"recorded_at"`
	RecordedTo        *time.Time            `json:"recorded_to,omitempty"`
	ExtractConf       float64               `json:"extract_conf"`
	ResolutionConf    float64               `json:"resolution_conf"`
	SourceQuality     float64               `json:"source_quality"`
	SupportCount      int                   `json:"support_count"`
	SourceGroupCount  int                   `json:"source_group_count"`
	Evidence          []EvidenceSpan        `json:"evidence"`
	Embedding         []float32             `json:"-"`
	EmbeddingModel    string                `json:"embedding_model,omitempty"`
	ExtractionModel   string                `json:"extraction_model,omitempty"`
	ExtractionVersion string                `json:"extraction_version,omitempty"`
	VerifierModel     string                `json:"verifier_model,omitempty"`
	PipelineRunID     string                `json:"pipeline_run_id,omitempty"`
	ProjectionVersion string                `json:"projection_version"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

func (a Assertion) Validate() error {
	switch {
	case strings.TrimSpace(a.AssertionID) == "":
		return errors.New("assertion_id is required")
	case strings.TrimSpace(a.ProfileID) == "":
		return errors.New("team_id is required")
	case strings.TrimSpace(a.SubjectEntityID) == "":
		return errors.New("subject_entity_id is required")
	case strings.TrimSpace(a.PredicateKey) == "":
		return errors.New("predicate_key is required")
	case strings.TrimSpace(a.RelationshipType) == "":
		return errors.New("relationship_type is required")
	case (strings.TrimSpace(a.ObjectEntityID) == "") == (a.ObjectValue == nil):
		return errors.New("exactly one object endpoint is required")
	case !a.Tier.IsValid():
		return errors.New("tier is invalid")
	case !a.Status.IsValid():
		return errors.New("status is invalid")
	case !a.PolicyFamily.IsValid():
		return errors.New("policy_family is invalid")
	case !a.Polarity.IsValid():
		return errors.New("polarity is invalid")
	case !a.Modality.IsValid():
		return errors.New("modality is invalid")
	case a.ValidFrom != nil && a.ValidTo != nil && !a.ValidTo.After(*a.ValidFrom):
		return errors.New("valid_to must be after valid_from")
	case a.ExtractConf < 0 || a.ExtractConf > 1:
		return errors.New("extract_conf must be between 0 and 1")
	case a.ResolutionConf < 0 || a.ResolutionConf > 1:
		return errors.New("resolution_conf must be between 0 and 1")
	case a.SourceQuality < 0 || a.SourceQuality > 1:
		return errors.New("source_quality must be between 0 and 1")
	case a.SupportCount < 1:
		return errors.New("support_count must be positive")
	case a.SourceGroupCount < 1 || a.SourceGroupCount > a.SupportCount:
		return errors.New("source_group_count must be positive and not exceed support_count")
	case len(a.Evidence) == 0:
		return errors.New("evidence is required")
	}
	if a.ObjectValue != nil {
		if err := a.ObjectValue.Validate(); err != nil {
			return err
		}
	}
	for _, evidence := range a.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}
