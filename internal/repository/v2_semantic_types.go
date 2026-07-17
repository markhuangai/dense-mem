package repository

import (
	"context"
	"time"
)

type V2SemanticRepository interface {
	CreateEntity(ctx context.Context, input V2CreateEntityInput) (*V2EntityRecord, error)
	AddEntityName(ctx context.Context, input V2AddEntityNameInput) (string, error)
	UpsertValue(ctx context.Context, input V2UpsertValueInput) (*V2ValueRecord, error)
	ApplyRelationshipDecision(ctx context.Context, input V2ApplyRelationshipDecisionInput) (*V2RelationshipDecisionResult, error)
	RetractRelationship(ctx context.Context, input V2RetractRelationshipInput) error
	AppendCrossReference(ctx context.Context, input V2AppendCrossReferenceInput) (string, error)
	CreateHypothesis(ctx context.Context, input V2CreateHypothesisInput) (string, error)
	ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]V2SemanticEdge, error)
}

type V2CreateEntityInput struct {
	TeamID          string
	OwnerProfileID  string
	EntityKind      string
	CanonicalName   string
	IdentityContext map[string]any
	Metadata        map[string]any
}

type V2AddEntityNameInput struct {
	TeamID         string
	OwnerProfileID string
	EntityID       string
	DisplayName    string
	NameKind       string
	Locale         string
	Metadata       map[string]any
}

type V2EntityRecord struct {
	TeamID        string
	EntityID      string
	EntityKind    string
	CanonicalName string
	Version       int
}

type V2UpsertValueInput struct {
	TeamID               string
	OwnerProfileID       string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Metadata             map[string]any
}

type V2ValueRecord struct {
	TeamID               string
	ValueID              string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Existing             bool
}

type V2EvidenceSupportInput struct {
	FragmentID       string
	SourceGroupKey   string
	SourceID         string
	SourceRevisionID string
	SpanStart        int
	SpanEnd          int
	Quote            string
	Authority        string
	Metadata         map[string]any
}

type V2ApplyRelationshipDecisionInput struct {
	TeamID               string
	OwnerProfileID       string
	IngestID             string
	PlacementItemID      string
	SubjectRef           string
	SubjectEntityID      string
	OriginalPredicate    string
	PredicateKey         string
	PredicateVersion     int
	ObjectRef            string
	ObjectEntityID       string
	ObjectValueID        string
	Polarity             string
	ScopeKey             string
	ValidFrom            *time.Time
	ValidTo              *time.Time
	EvidenceVerdict      string
	PromoteToFact        bool
	Confidence           *float64
	Rationale            string
	Model                string
	ResponseHash         string
	Support              *V2EvidenceSupportInput
	ObservationMetadata  map[string]any
	RelationshipMetadata map[string]any
}

type V2RelationshipRecord struct {
	TeamID             string
	RelationshipID     string
	OwnerProfileID     string
	SemanticGroupKey   string
	SubjectEntityID    string
	PredicateKey       string
	PredicateVersion   int
	ObjectEntityID     string
	ObjectValueID      string
	RelationshipKind   string
	CurrentCardinality string
	Tier               string
	Status             string
	Polarity           string
	ScopeKey           string
	SupportCount       int
	SourceGroupCount   int
	Version            int
}

type V2RelationshipDecisionResult struct {
	Relationship        *V2RelationshipRecord
	ObservationID       string
	VerificationEventID string
	SupportID           string
	SupportDecisionID   string
	ReviewTaskID        string
	CreatedRelationship bool
}

type V2RetractRelationshipInput struct {
	TeamID         string
	OwnerProfileID string
	RelationshipID string
	Reason         string
}

type V2AppendCrossReferenceInput struct {
	TeamID                    string
	AuthorProfileID           string
	SourceRelationshipID      string
	SourceRelationshipVersion int
	TargetRelationshipID      string
	TargetRelationshipVersion int
	Kind                      string
	VerificationEventID       string
	Metadata                  map[string]any
}

type V2CreateHypothesisInput struct {
	TeamID         string
	OwnerProfileID string
	Status         string
	Payload        map[string]any
}

type V2SemanticEdge struct {
	TeamID             string
	RelationshipID     string
	OwnerProfileID     string
	SemanticGroupKey   string
	SubjectEntityID    string
	PredicateKey       string
	PredicateVersion   int
	ObjectEntityID     string
	ObjectValueID      string
	RelationshipKind   string
	CurrentCardinality string
	Polarity           string
	ScopeKey           string
	Tier               string
	SupportCount       int
	SourceGroupCount   int
	Version            int
}
