package contract

import (
	"context"
	"errors"
	"time"
)

const (
	MaxChanges                 = 64
	MaxMembers                 = 128
	MaxAliases                 = 20
	MaxPageSize                = 200
	MaxParentDepth             = 32
	MaxPublicationBytes        = 1 << 20
	MaxPublicationDependencies = 512
	FingerprintVersion         = "ontology-source-v1"
)

type Kind string

const (
	EntityClass       Kind = "entity_class"
	PredicateConcept  Kind = "predicate_concept"
	Topic             Kind = "topic"
	AssignmentKind    Kind = "assignment"
	EvidenceGroup     Kind = "evidence_group"
	RelationshipGroup Kind = "relationship_group"
	OverrideKind      Kind = "override"
)

type SourceKind string

const (
	EntitySource       SourceKind = "entity"
	PredicateSource    SourceKind = "predicate"
	EvidenceSource     SourceKind = "evidence"
	RelationshipSource SourceKind = "relationship"
)

var (
	ErrInvalid      = errors.New("invalid ontology input")
	ErrConflict     = errors.New("ontology revision or operation conflict")
	ErrSourceStale  = errors.New("ontology source is unavailable or stale")
	ErrNotFound     = errors.New("ontology record not found")
	ErrUnauthorized = errors.New("ontology operation is not authorized")
	ErrOverride     = errors.New("ontology change conflicts with manager override")
)

type SourceHandle struct {
	Kind    SourceKind `json:"kind"`
	ID      string     `json:"id"`
	Version int64      `json:"version"`
}

type SourceDependency struct {
	SourceHandle
	Fingerprint string `json:"fingerprint"`
}

type RevisionRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type Definition struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	Description    string   `json:"description"`
	Aliases        []string `json:"aliases,omitempty"`
	ParentID       string   `json:"parent_id,omitempty"`
	BaseEntityKind string   `json:"base_entity_kind,omitempty"`
}

type Assignment struct {
	Source       SourceHandle `json:"source"`
	DefinitionID string       `json:"definition_id"`
}

type Group struct {
	Members      []SourceHandle `json:"members"`
	AssessmentID string         `json:"assessment_id,omitempty"`
}

type OverrideAction string

const (
	PinDefinition     OverrideAction = "pin_definition"
	SetClassification OverrideAction = "set_classification"
	GroupTogether     OverrideAction = "group_together"
	KeepSeparate      OverrideAction = "keep_separate"
)

type Override struct {
	Action       OverrideAction `json:"action"`
	TargetID     string         `json:"target_id,omitempty"`
	DefinitionID string         `json:"definition_id,omitempty"`
	Members      []SourceHandle `json:"members,omitempty"`
}

type Record struct {
	ID           string             `json:"id"`
	Version      int64              `json:"version"`
	Kind         Kind               `json:"kind"`
	Retired      bool               `json:"retired"`
	Definition   *Definition        `json:"definition,omitempty"`
	Assignment   *Assignment        `json:"assignment,omitempty"`
	Group        *Group             `json:"group,omitempty"`
	Override     *Override          `json:"override,omitempty"`
	Sources      []SourceDependency `json:"sources,omitempty"`
	Dependencies []RevisionRef      `json:"dependencies,omitempty"`
	Fingerprint  string             `json:"fingerprint,omitempty"`
}

type Change struct {
	ExpectedVersion int64  `json:"expected_version"`
	Record          Record `json:"record"`
}

type Publication struct {
	OperationKey     string   `json:"operation_key"`
	ExpectedRevision int64    `json:"expected_revision"`
	Reason           string   `json:"reason"`
	Changes          []Change `json:"changes"`
	RollbackOf       string   `json:"rollback_of,omitempty"`
}

type PublicationResult struct {
	ID            string        `json:"id"`
	Revision      int64         `json:"revision"`
	Existing      bool          `json:"existing"`
	Records       []RevisionRef `json:"records"`
	NextPredicate string        `json:"next_predicate,omitempty"`
}

type RecordView struct {
	Record
	Current     bool   `json:"current"`
	StaleReason string `json:"stale_reason,omitempty"`
}

type Page struct {
	Records  []RecordView
	NextID   string
	Revision int64
}

type SeedInput struct {
	OperationKey     string
	ExpectedRevision int64
	AfterPredicate   string
	Limit            int
}

type SeedResult struct {
	PublicationResult
}

type SourceSnapshot struct {
	SourceHandle
	TeamID     string            `json:"team_id"`
	SpaceID    string            `json:"space_id"`
	Generation int64             `json:"generation"`
	OwnerID    string            `json:"owner_id,omitempty"`
	Eligible   bool              `json:"eligible"`
	EntityKind string            `json:"entity_kind,omitempty"`
	State      map[string]string `json:"state"`
	MeaningKey string            `json:"meaning_key,omitempty"`
}

type PublicationHistory struct {
	PublicationResult
	Origin    string
	ActorID   string
	Reason    string
	CreatedAt time.Time
}

type Repository interface {
	PublishAutomatic(context.Context, string, Publication) (PublicationResult, error)
	PublishManager(context.Context, string, Publication) (PublicationResult, error)
	SeedDefinitions(context.Context, string, SeedInput) (SeedResult, error)
	GetRecord(context.Context, string, string, int64) (RecordView, error)
	ListRecords(context.Context, string, Kind, string, int) (Page, error)
	ReadSources(context.Context, string, []SourceHandle) ([]SourceSnapshot, error)
	Rollback(context.Context, string, string, string, int64, string) (PublicationResult, error)
	History(context.Context, string, int64, int) ([]PublicationHistory, error)
}
