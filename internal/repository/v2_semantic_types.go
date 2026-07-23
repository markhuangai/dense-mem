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
	RetractRelationship(ctx context.Context, input V2RetractRelationshipInput) (*V2RelationshipTransitionResult, error)
	ApplyRelationshipSupportDecision(ctx context.Context, input V2ApplyRelationshipSupportDecisionInput) (*V2RelationshipSupportDecisionResult, error)
	CorrectEntityResolution(ctx context.Context, input V2CorrectEntityResolutionInput) (*V2CorrectEntityResolutionResult, error)
	AppendCrossReference(ctx context.Context, input V2AppendCrossReferenceInput) (string, error)
	CreateHypothesis(ctx context.Context, input V2CreateHypothesisInput) (string, error)
	ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]V2SemanticEdge, error)
	TraceRelationship(ctx context.Context, input V2TraceRelationshipInput) (*V2RelationshipTraceResult, error)
	SemanticGraph(ctx context.Context, input V2SemanticGraphQuery) (*V2SemanticGraphSnapshot, error)
	SemanticGraphNodeDetail(ctx context.Context, input V2SemanticGraphNodeDetailInput) (*V2SemanticGraphNode, error)
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

type V2SemanticReviewEntityCandidateInput struct {
	TeamID         string
	OwnerProfileID string
	Name           string
	EntityKind     string
	KnownEntityID  string
	Limit          int
}

type V2SemanticReviewEntityCandidate struct {
	TeamID          string
	EntityID        string
	EntityKind      string
	CanonicalName   string
	IdentityContext map[string]any
	Status          string
}

type V2SemanticReviewPredicateCandidateInput struct {
	TeamID         string
	OwnerProfileID string
	Predicate      string
	Limit          int
}

type V2SemanticReviewPredicateResolutionInput struct {
	TeamID         string
	OwnerProfileID string
	Predicates     []string
	Limit          int
}

type V2SemanticReviewPredicateResolution struct {
	RequestedPredicate string
	MatchKind          string
	Candidate          V2SemanticReviewPredicateCandidate
}

type V2SemanticReviewPredicateOptionsInput struct {
	TeamID         string
	OwnerProfileID string
	QueryText      string
	Limit          int
}

type V2EnsureSemanticPredicateCandidateInput struct {
	TeamID           string
	OwnerProfileID   string
	Predicate        string
	RelationshipKind string
	SubjectKind      string
	ObjectKind       string
	Origin           string
	Metadata         map[string]any
}

type V2SemanticReviewPredicateCandidate struct {
	PredicateKey        string
	Version             int
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
	LifecycleState      string
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
	ProposalRef          string
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
	ProposalID          string
	OwnerProfileID      string
	Category            string
	Reason              string
	CreatedRelationship bool
}

type V2RetractRelationshipInput struct {
	TeamID         string
	OwnerProfileID string
	RelationshipID string
	Reason         string
	IdempotencyKey string
}

type V2RelationshipTransitionResult struct {
	TeamID         string
	TransitionID   string
	RelationshipID string
	FromTier       string
	FromStatus     string
	ToTier         string
	ToStatus       string
	IdempotencyKey string
}

type V2CorrectionEvidenceInput struct {
	Content     string
	SourceType  string
	Authority   string
	SourceGroup string
	Metadata    map[string]any
}

type V2CorrectEntityResolutionInput struct {
	TeamID                 string
	OwnerProfileID         string
	Action                 string
	SourceEntityID         string
	TargetEntityID         string
	SelectedObservationIDs []string
	DryRun                 bool
	PlanToken              string
	Evidence               []V2CorrectionEvidenceInput
	IdempotencyKey         string
}

type V2CorrectEntityResolutionResult struct {
	DryRun                 bool     `json:"dry_run"`
	PlanToken              string   `json:"plan_token,omitempty"`
	SelectedObservationIDs []string `json:"selected_ids"`
	BlockedObservationIDs  []string `json:"blocked_ids"`
	ImpactSummary          string   `json:"impact_summary"`
	CorrectionEventID      string   `json:"correction_event_id,omitempty"`
	NewEntityID            string   `json:"new_entity_id,omitempty"`
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

type V2TraceRelationshipInput struct {
	TeamID                  string
	RelationshipID          string
	IncludeEvidenceContent  *bool
	IncludeVerification     *bool
	IncludeTransitions      *bool
	MaxDepth                int
	MaxEdges                int
	MaxEvents               int
	MaxFragmentContentRunes int
	PredicateKeys           []string
	Topic                   string
	MinRelevance            *float64
}

type V2RelationshipTraceResult struct {
	Relationship           *V2RelationshipTraceRecord
	Observations           []V2RelationshipObservationRecord
	EvidenceSupports       []V2RelationshipEvidenceSupportRecord
	SupportDecisionEvents  []V2RelationshipSupportDecisionEvent
	EvidenceFragments      []V2TraceEvidenceFragment
	VerificationEvents     []V2RelationshipVerificationEvent
	Transitions            []V2RelationshipTransitionEvent
	CrossProfileReferences []V2RelationshipCrossReferenceRecord
	IdentityCorrections    []V2EntityCorrectionEventRecord
	SupersessionLineage    []V2RelationshipTraceRecord
	SearchDocuments        []V2TraceSearchDocument
	EmbeddingJobs          []V2TraceEmbeddingJob
	SemanticNodes          []V2SemanticGraphNode
	SemanticEdges          []V2SemanticGraphEdge
	VisitedEntityIDs       []string
	StoppedReason          string
	Truncated              bool
}

type V2RelationshipTraceRecord struct {
	TeamID             string     `json:"team_id,omitempty"`
	RelationshipID     string     `json:"relationship_id,omitempty"`
	OwnerProfileID     string     `json:"owner_profile_id,omitempty"`
	SemanticGroupKey   string     `json:"semantic_group_key,omitempty"`
	SubjectEntityID    string     `json:"subject_entity_id,omitempty"`
	SubjectName        string     `json:"subject_name,omitempty"`
	SubjectKind        string     `json:"subject_kind,omitempty"`
	PredicateKey       string     `json:"predicate_key,omitempty"`
	PredicateVersion   int        `json:"predicate_version,omitempty"`
	ObjectEntityID     string     `json:"object_entity_id,omitempty"`
	ObjectEntityName   string     `json:"object_entity_name,omitempty"`
	ObjectEntityKind   string     `json:"object_entity_kind,omitempty"`
	ObjectValueID      string     `json:"object_value_id,omitempty"`
	ObjectValue        string     `json:"object_value,omitempty"`
	ObjectValueType    string     `json:"object_value_type,omitempty"`
	RelationshipKind   string     `json:"relationship_kind,omitempty"`
	CurrentCardinality string     `json:"current_cardinality,omitempty"`
	Tier               string     `json:"tier,omitempty"`
	Status             string     `json:"status,omitempty"`
	Polarity           string     `json:"polarity,omitempty"`
	ScopeKey           string     `json:"scope_key,omitempty"`
	ValidFrom          *time.Time `json:"valid_from,omitempty"`
	ValidTo            *time.Time `json:"valid_to,omitempty"`
	SupportCount       int        `json:"support_count,omitempty"`
	SourceGroupCount   int        `json:"source_group_count,omitempty"`
	Version            int        `json:"version,omitempty"`
	CreatedAt          time.Time  `json:"created_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at,omitempty"`
	RecordedTo         *time.Time `json:"recorded_to,omitempty"`
}

type V2RelationshipObservationRecord struct {
	ObservationID     string         `json:"observation_id,omitempty"`
	RelationshipID    string         `json:"relationship_id,omitempty"`
	IngestID          string         `json:"ingest_id,omitempty"`
	PlacementItemID   string         `json:"placement_item_id,omitempty"`
	OwnerProfileID    string         `json:"owner_profile_id,omitempty"`
	SubjectRef        string         `json:"subject_ref,omitempty"`
	OriginalPredicate string         `json:"original_predicate,omitempty"`
	ObjectRef         string         `json:"object_ref,omitempty"`
	SubjectEntityID   string         `json:"subject_entity_id,omitempty"`
	PredicateKey      string         `json:"predicate_key,omitempty"`
	PredicateVersion  int            `json:"predicate_version,omitempty"`
	ObjectEntityID    string         `json:"object_entity_id,omitempty"`
	ObjectValueID     string         `json:"object_value_id,omitempty"`
	Polarity          string         `json:"polarity,omitempty"`
	ScopeKey          string         `json:"scope_key,omitempty"`
	ValidFrom         *time.Time     `json:"valid_from,omitempty"`
	ValidTo           *time.Time     `json:"valid_to,omitempty"`
	Evidence          any            `json:"evidence,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"created_at,omitempty"`
}

type V2RelationshipVerificationEvent struct {
	VerificationEventID string         `json:"verification_event_id,omitempty"`
	ObservationID       string         `json:"observation_id,omitempty"`
	OwnerProfileID      string         `json:"owner_profile_id,omitempty"`
	EvidenceVerdict     string         `json:"evidence_verdict,omitempty"`
	Confidence          *float64       `json:"confidence,omitempty"`
	Rationale           string         `json:"rationale,omitempty"`
	Model               string         `json:"model,omitempty"`
	ResponseHash        string         `json:"response_hash,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at,omitempty"`
}

type V2RelationshipEvidenceSupportRecord struct {
	SupportID           string         `json:"support_id,omitempty"`
	RelationshipID      string         `json:"relationship_id,omitempty"`
	ObservationID       string         `json:"observation_id,omitempty"`
	VerificationEventID string         `json:"verification_event_id,omitempty"`
	FragmentID          string         `json:"fragment_id,omitempty"`
	OwnerProfileID      string         `json:"owner_profile_id,omitempty"`
	SourceGroupKey      string         `json:"source_group_key,omitempty"`
	SourceID            string         `json:"source_id,omitempty"`
	SourceRevisionID    string         `json:"source_revision_id,omitempty"`
	SpanStart           int            `json:"span_start,omitempty"`
	SpanEnd             int            `json:"span_end,omitempty"`
	Quote               string         `json:"quote,omitempty"`
	Authority           string         `json:"authority,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at,omitempty"`
}

type V2RelationshipSupportDecisionEvent struct {
	SupportDecisionID string         `json:"support_decision_id,omitempty"`
	SupportID         string         `json:"support_id,omitempty"`
	RelationshipID    string         `json:"relationship_id,omitempty"`
	OwnerProfileID    string         `json:"owner_profile_id,omitempty"`
	ActorProfileID    string         `json:"actor_profile_id,omitempty"`
	Decision          string         `json:"decision,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"created_at,omitempty"`
}

type V2ApplyRelationshipSupportDecisionInput struct {
	TeamID         string
	OwnerProfileID string
	RelationshipID string
	SupportID      string
	Decision       string
	Reason         string
	IdempotencyKey string
	Metadata       map[string]any
}

type V2RelationshipSupportDecisionResult struct {
	TeamID            string
	SupportDecisionID string
	SupportID         string
	RelationshipID    string
	Decision          string
	IdempotencyKey    string
	FromTier          string
	FromStatus        string
	ToTier            string
	ToStatus          string
	SupportCount      int
	SourceGroupCount  int
}

type V2TraceEvidenceFragment struct {
	FragmentID        string         `json:"fragment_id,omitempty"`
	IngestID          string         `json:"ingest_id,omitempty"`
	OwnerProfileID    string         `json:"owner_profile_id,omitempty"`
	SourceID          string         `json:"source_id,omitempty"`
	SourceRevisionID  string         `json:"source_revision_id,omitempty"`
	SourceKey         string         `json:"source_key,omitempty"`
	SourceKind        string         `json:"source_kind,omitempty"`
	RevisionToken     string         `json:"revision_token,omitempty"`
	CurrentRevisionID string         `json:"current_revision_id,omitempty"`
	EvidenceIndex     int            `json:"evidence_index,omitempty"`
	Content           string         `json:"content,omitempty"`
	ContentHash       string         `json:"content_hash,omitempty"`
	ContentTruncated  bool           `json:"content_truncated,omitempty"`
	SourceType        string         `json:"source_type,omitempty"`
	Authority         string         `json:"authority,omitempty"`
	SourceRef         string         `json:"source_ref,omitempty"`
	Labels            []string       `json:"labels,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"created_at,omitempty"`
}

type V2RelationshipTransitionEvent struct {
	TransitionID        string         `json:"transition_id,omitempty"`
	RelationshipID      string         `json:"relationship_id,omitempty"`
	OwnerProfileID      string         `json:"owner_profile_id,omitempty"`
	FromTier            string         `json:"from_tier,omitempty"`
	FromStatus          string         `json:"from_status,omitempty"`
	ToTier              string         `json:"to_tier,omitempty"`
	ToStatus            string         `json:"to_status,omitempty"`
	Reason              string         `json:"reason,omitempty"`
	VerificationEventID string         `json:"verification_event_id,omitempty"`
	SupportDecisionID   string         `json:"support_decision_id,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at,omitempty"`
}

type V2RelationshipCrossReferenceRecord struct {
	CrossReferenceID          string         `json:"cross_reference_id,omitempty"`
	AuthorProfileID           string         `json:"author_profile_id,omitempty"`
	SourceRelationshipID      string         `json:"source_relationship_id,omitempty"`
	SourceRelationshipVersion int            `json:"source_relationship_version,omitempty"`
	TargetRelationshipID      string         `json:"target_relationship_id,omitempty"`
	TargetRelationshipVersion int            `json:"target_relationship_version,omitempty"`
	Kind                      string         `json:"kind,omitempty"`
	VerificationEventID       string         `json:"verification_event_id,omitempty"`
	Metadata                  map[string]any `json:"metadata,omitempty"`
	CreatedAt                 time.Time      `json:"created_at,omitempty"`
}

type V2EntityCorrectionEventRecord struct {
	CorrectionEventID      string         `json:"correction_event_id,omitempty"`
	OwnerProfileID         string         `json:"owner_profile_id,omitempty"`
	Action                 string         `json:"action,omitempty"`
	SurvivorEntityID       string         `json:"survivor_entity_id,omitempty"`
	NewEntityID            string         `json:"new_entity_id,omitempty"`
	SelectedObservationIDs []string       `json:"selected_observation_ids,omitempty"`
	Reason                 string         `json:"reason,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
	CreatedAt              time.Time      `json:"created_at,omitempty"`
}

type V2TraceSearchDocument struct {
	SearchDocumentID    string    `json:"search_document_id,omitempty"`
	OwnerProfileID      string    `json:"owner_profile_id,omitempty"`
	SourceKind          string    `json:"source_kind,omitempty"`
	SourceID            string    `json:"source_id,omitempty"`
	SourceVersion       int64     `json:"source_version,omitempty"`
	DocumentVersion     int64     `json:"document_version,omitempty"`
	EmbeddingContractID string    `json:"embedding_contract_id,omitempty"`
	EmbeddingDimensions int       `json:"embedding_dimensions,omitempty"`
	SearchState         string    `json:"search_state,omitempty"`
	DocumentHash        string    `json:"document_hash,omitempty"`
	CreatedAt           time.Time `json:"created_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type V2TraceEmbeddingJob struct {
	EmbeddingJobID      string     `json:"embedding_job_id,omitempty"`
	SearchDocumentID    string     `json:"search_document_id,omitempty"`
	OwnerProfileID      string     `json:"owner_profile_id,omitempty"`
	SourceKind          string     `json:"source_kind,omitempty"`
	SourceID            string     `json:"source_id,omitempty"`
	SourceVersion       int64      `json:"source_version,omitempty"`
	DocumentVersion     int64      `json:"document_version,omitempty"`
	EmbeddingContractID string     `json:"embedding_contract_id,omitempty"`
	EmbeddingDimensions int        `json:"embedding_dimensions,omitempty"`
	Status              string     `json:"status,omitempty"`
	Attempts            int        `json:"attempts,omitempty"`
	Error               string     `json:"error,omitempty"`
	CreatedAt           time.Time  `json:"created_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
}

type V2SemanticGraphQuery struct {
	TeamID       string
	Scope        string
	Query        string
	Types        []string
	AnchorType   string
	AnchorID     string
	Depth        int
	Limit        int
	MinRelevance float64
}

type V2SemanticGraphNodeDetailInput struct {
	TeamID   string
	NodeType string
	NodeID   string
}

type V2SemanticGraphSnapshot struct {
	Scope     string                 `json:"scope,omitempty"`
	Query     string                 `json:"query,omitempty"`
	Anchor    *V2SemanticGraphAnchor `json:"anchor,omitempty"`
	Depth     int                    `json:"depth,omitempty"`
	Limit     int                    `json:"limit,omitempty"`
	Truncated bool                   `json:"truncated,omitempty"`
	Nodes     []V2SemanticGraphNode  `json:"nodes,omitempty"`
	Edges     []V2SemanticGraphEdge  `json:"edges,omitempty"`
}

type V2SemanticGraphAnchor struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Key  string `json:"key,omitempty"`
}

type V2SemanticGraphNode struct {
	Key            string     `json:"key,omitempty"`
	ID             string     `json:"id,omitempty"`
	Type           string     `json:"type,omitempty"`
	Title          string     `json:"title,omitempty"`
	Body           string     `json:"body,omitempty"`
	Status         string     `json:"status,omitempty"`
	OwnerProfileID string     `json:"owner_profile_id,omitempty"`
	RecordedAt     *time.Time `json:"recorded_at,omitempty"`
}

type V2SemanticGraphEdge struct {
	ID               string `json:"id,omitempty"`
	RelationshipID   string `json:"relationship_id,omitempty"`
	Source           string `json:"source,omitempty"`
	Target           string `json:"target,omitempty"`
	Relationship     string `json:"relationship,omitempty"`
	Directed         bool   `json:"directed,omitempty"`
	OwnerProfileID   string `json:"owner_profile_id,omitempty"`
	Tier             string `json:"tier,omitempty"`
	SupportCount     int    `json:"support_count,omitempty"`
	SourceGroupCount int    `json:"source_group_count,omitempty"`
}
