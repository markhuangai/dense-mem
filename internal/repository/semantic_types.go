package repository

import (
	"context"
	"time"
)

type SemanticRepository interface {
	CreateEntity(ctx context.Context, input CreateEntityInput) (*EntityRecord, error)
	AddEntityName(ctx context.Context, input AddEntityNameInput) (string, error)
	UpsertValue(ctx context.Context, input UpsertValueInput) (*ValueRecord, error)
	ApplyRelationshipDecision(ctx context.Context, input ApplyRelationshipDecisionInput) (*RelationshipDecisionResult, error)
	RetractRelationship(ctx context.Context, input RetractRelationshipInput) (*RelationshipTransitionResult, error)
	ApplyRelationshipSupportDecision(ctx context.Context, input ApplyRelationshipSupportDecisionInput) (*RelationshipSupportDecisionResult, error)
	CorrectRelationship(ctx context.Context, input CorrectRelationshipInput) (*CorrectRelationshipResult, error)
	PlanRelationshipCorrectionEmbeddings(ctx context.Context, input CorrectRelationshipInput) (*RelationshipCorrectionEmbeddingPlan, error)
	CorrectRelationshipWithEmbeddings(ctx context.Context, input CorrectRelationshipInput, embeddings []RelationshipCorrectionEmbedding) (*CorrectRelationshipResult, error)
	GetRelationshipCorrection(ctx context.Context, input GetRelationshipCorrectionInput) (*RelationshipCorrectionStatus, error)
	AppendCrossReference(ctx context.Context, input AppendCrossReferenceInput) (string, error)
	CreateHypothesis(ctx context.Context, input CreateHypothesisInput) (string, error)
	ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]SemanticEdge, error)
	TraceRelationship(ctx context.Context, input TraceRelationshipInput) (*RelationshipTraceResult, error)
	SemanticGraph(ctx context.Context, input SemanticGraphQuery) (*SemanticGraphSnapshot, error)
	SemanticGraphNodeDetail(ctx context.Context, input SemanticGraphNodeDetailInput) (*SemanticGraphNode, error)
}

type CreateEntityInput struct {
	TeamID          string
	OwnerProfileID  string
	EntityKind      string
	CanonicalName   string
	IdentityContext map[string]any
	Metadata        map[string]any
}

type AddEntityNameInput struct {
	TeamID         string
	OwnerProfileID string
	EntityID       string
	DisplayName    string
	NameKind       string
	Locale         string
	Metadata       map[string]any
}

type EntityRecord struct {
	TeamID        string
	EntityID      string
	EntityKind    string
	CanonicalName string
	Version       int
}

type SemanticReviewEntityCandidateInput struct {
	TeamID         string
	OwnerProfileID string
	Name           string
	EntityKind     string
	KnownEntityID  string
	Limit          int
}

type SemanticReviewEntityCandidate struct {
	TeamID          string
	EntityID        string
	EntityKind      string
	CanonicalName   string
	ActiveNames     []string
	IdentityContext map[string]any
	Status          string
}

// SemanticAssessmentEntityMatch is a current, team-scoped canonical or alias
// name that matched evidence text. The service verifies rune-token boundaries
// before turning it into a candidate group.
type SemanticAssessmentEntityMatch struct {
	Candidate   SemanticReviewEntityCandidate
	MatchedName string
}

type SemanticAssessmentEntityMatchInput struct {
	TeamID         string
	OwnerProfileID string
	EvidenceText   string
	Limit          int
}

type SemanticAssessmentEntityMatchResult struct {
	Matches   []SemanticAssessmentEntityMatch
	Truncated bool
}

type SemanticAssessmentKnownEntityInput struct {
	TeamID         string
	OwnerProfileID string
	EntityIDs      []string
}

// SubmissionAssessmentKnownEvidence is an immutable, request-scoped snapshot
// of explicitly requested evidence that passed the authenticated visibility
// and lifecycle eligibility fence. It is read-only assessment context; it is
// never treated as submitted evidence or security input.
type SubmissionAssessmentKnownEvidence struct {
	TeamID                  string
	EvidenceID              string
	FragmentID              string
	IngestID                string
	OwnerProfileID          string
	Content                 string
	ContentHash             string
	Authority               string
	SourceID                string
	SourceRevisionID        string
	CurrentSourceRevisionID string
	SpaceID                 string
	SpaceGeneration         int64
}

type SubmissionAssessmentKnownEvidenceInput struct {
	TeamID         string
	OwnerProfileID string
	// SpaceID scopes known evidence to the memory space receiving the submission.
	// Empty retains the team-shared default for direct repository callers.
	SpaceID     string
	EvidenceIDs []string
}

type SubmissionAssessmentKnownEvidenceResult struct {
	Evidence []SubmissionAssessmentKnownEvidence
}

type SemanticReviewPredicateCandidateInput struct {
	TeamID         string
	OwnerProfileID string
	Predicate      string
	Limit          int
}

type SemanticReviewPredicateResolutionInput struct {
	TeamID         string
	OwnerProfileID string
	Predicates     []string
	Limit          int
}

type SemanticReviewPredicateResolution struct {
	RequestedPredicate string
	MatchKind          string
	Candidate          SemanticReviewPredicateCandidate
}

type SemanticReviewPredicateOptionsInput struct {
	TeamID         string
	OwnerProfileID string
	QueryText      string
	Limit          int
}

type EnsureSemanticPredicateCandidateInput struct {
	TeamID           string
	OwnerProfileID   string
	Predicate        string
	RelationshipKind string
	SubjectKind      string
	ObjectKind       string
	Origin           string
	Metadata         map[string]any
}

type SemanticReviewPredicateCandidate struct {
	PredicateKey        string
	Version             int
	Aliases             []string
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
	LifecycleState      string
}

type SemanticAssessmentPredicateOptionsInput struct {
	TeamID         string
	OwnerProfileID string
	QueryText      string
	ProposedKeys   []string
	Limit          int
}

// SubmissionAssessmentEntityCatalogInput loads the complete exact-name and
// known-id candidate set for every server-derived submission entity target.
// A result is marked incomplete rather than silently trimming a target's
// candidates.
type SubmissionAssessmentEntityCatalogInput struct {
	TeamID         string
	OwnerProfileID string
	// SpaceID scopes candidates to the memory space receiving the submission.
	// Empty retains the historical team-shared default for direct repository callers.
	SpaceID        string
	Entities       []SubmissionAssessmentEntityCatalogTarget
	CandidateLimit int
}

type SubmissionAssessmentEntityCatalogTarget struct {
	Ref           string
	Surface       string
	EntityKind    string
	KnownEntityID string
}

type SubmissionAssessmentEntityCatalogGroup struct {
	Ref        string
	Candidates []SemanticReviewEntityCandidate
	Complete   bool
}

type SubmissionAssessmentEntityCatalogResult struct {
	Groups   []SubmissionAssessmentEntityCatalogGroup
	Complete bool
}

type UpsertValueInput struct {
	TeamID               string
	OwnerProfileID       string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Metadata             map[string]any
}

type ValueRecord struct {
	TeamID               string
	ValueID              string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Existing             bool
}

type EvidenceSupportInput struct {
	FragmentID             string
	EvidenceOwnerProfileID string
	SourceGroupKey         string
	SourceID               string
	SourceRevisionID       string
	SpanStart              int
	SpanEnd                int
	Quote                  string
	Authority              string
	Metadata               map[string]any
}

type ApplyRelationshipDecisionInput struct {
	TeamID                  string
	OwnerProfileID          string
	IngestID                string
	ProposalRef             string
	SubjectRef              string
	SubjectEntityID         string
	OriginalPredicate       string
	PredicateKey            string
	PredicateVersion        int
	ObjectRef               string
	ObjectEntityID          string
	ObjectValueID           string
	Polarity                string
	ScopeKey                string
	ValidFrom               *time.Time
	ValidTo                 *time.Time
	EvidenceVerdict         string
	AssessorAccepted        bool
	PromoteToFact           bool
	Confidence              *float64
	Rationale               string
	Model                   string
	ResponseHash            string
	Support                 *EvidenceSupportInput
	Supports                []EvidenceSupportInput
	ObservationMetadata     map[string]any
	RelationshipMetadata    map[string]any
	AssessmentID            string
	AssessmentPolicyVersion string
	ThresholdUsed           *float64
	GateResult              string
	SuppressSupport         bool
}

type RelationshipRecord struct {
	TeamID             string
	RelationshipID     string
	SpaceID            string `json:"-"`
	SpaceGeneration    int64  `json:"-"`
	OwnerProfileID     string
	SemanticGroupKey   string
	SubjectEntityID    string
	PredicateKey       string
	PredicateVersion   int
	ObjectEntityID     string
	ObjectValueID      string
	RelationshipKind   string
	CurrentCardinality string
	Status             string
	Polarity           string
	ScopeKey           string
	ValidFrom          *time.Time
	ValidTo            *time.Time
	IdentityAliasOfID  string
	SupportCount       int
	SourceGroupCount   int
	Version            int
}

type RelationshipDecisionResult struct {
	Relationship        *RelationshipRecord
	ObservationID       string
	VerificationEventID string
	SupportID           string
	SupportIDs          []string
	SupportDecisionID   string
	ProposalID          string
	OwnerProfileID      string
	Category            string
	Reason              string
	ConfidenceGate      string
	PolicyVersion       string
	CreatedRelationship bool
}

type RetractRelationshipInput struct {
	TeamID         string
	OwnerProfileID string
	RelationshipID string
	Reason         string
	IdempotencyKey string
}

type RelationshipTransitionResult struct {
	TeamID         string
	TransitionID   string
	RelationshipID string
	FromStatus     string
	ToStatus       string
	IdempotencyKey string
}

type RelationshipCorrectionEntityPatch struct {
	EntityID   string `json:"entity_id,omitempty"`
	Name       string `json:"name,omitempty"`
	EntityKind string `json:"entity_kind,omitempty"`
}

type RelationshipCorrectionPredicatePatch struct {
	Key string `json:"key"`
}

type RelationshipCorrectionPatch struct {
	SubjectEntity *RelationshipCorrectionEntityPatch    `json:"subject_entity,omitempty"`
	Predicate     *RelationshipCorrectionPredicatePatch `json:"predicate,omitempty"`
	ObjectEntity  *RelationshipCorrectionEntityPatch    `json:"object_entity,omitempty"`
}

type RelationshipCorrectionSupport struct {
	EvidenceID string `json:"evidence_id"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type RelationshipCorrectionSelection struct {
	SubjectEntityID string `json:"subject_entity_id,omitempty"`
	ObjectEntityID  string `json:"object_entity_id,omitempty"`
}

type CorrectRelationshipInput struct {
	TeamID            string
	OwnerProfileID    string
	Action            string
	RelationshipID    string
	ExpectedVersion   int
	Patch             RelationshipCorrectionPatch
	Supports          []RelationshipCorrectionSupport
	Reason            string
	SubmissionID      string
	ConfirmationToken string
	Selection         RelationshipCorrectionSelection
	IdempotencyKey    string
}

type RelationshipCorrectionCandidate struct {
	Endpoint      string `json:"endpoint"`
	EntityID      string `json:"entity_id"`
	EntityKind    string `json:"entity_kind"`
	CanonicalName string `json:"canonical_name"`
}

type RelationshipCorrectionConfirmation struct {
	Token      string                            `json:"token"`
	ExpiresAt  time.Time                         `json:"expires_at"`
	Candidates []RelationshipCorrectionCandidate `json:"candidates"`
}

type RelationshipCorrectionResult struct {
	OriginalRelationshipID  string `json:"original_relationship_id"`
	OriginalVersion         int    `json:"original_version"`
	SuccessorRelationshipID string `json:"successor_relationship_id"`
	SuccessorVersion        int    `json:"successor_version"`
	ReusedSuccessor         bool   `json:"reused_successor"`
}

type CorrectRelationshipResult struct {
	SubmissionID    string
	ProcessingState string
	SearchState     string
	Confirmation    *RelationshipCorrectionConfirmation
	Correction      *RelationshipCorrectionResult
	ErrorCode       string
	ErrorMessage    string
}

// RelationshipCorrectionEmbeddingPlan is the read-only provider input for a
// correction. The commit phase identifies final rows by document hash.
type RelationshipCorrectionEmbeddingPlan struct {
	Documents               []RelationshipCorrectionEmbeddingDocument
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type RelationshipCorrectionEmbeddingDocument struct {
	DocumentHash string
	DocumentText string
}

// RelationshipCorrectionEmbedding is a validated provider result associated
// with the stable document hash from a correction plan.
type RelationshipCorrectionEmbedding struct {
	DocumentHash            string
	Embedding               []float32
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type GetRelationshipCorrectionInput struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
}

type RelationshipCorrectionStatus = CorrectRelationshipResult

type AppendCrossReferenceInput struct {
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

type CreateHypothesisInput struct {
	TeamID         string
	OwnerProfileID string
	Status         string
	Payload        map[string]any
}

type SemanticEdge struct {
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
	SupportCount       int
	SourceGroupCount   int
	Version            int
}

type TraceRelationshipInput struct {
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
	spaceID                 string
}

type RelationshipTraceResult struct {
	Relationship            *RelationshipTraceRecord
	Observations            []RelationshipObservationRecord
	EvidenceSupports        []RelationshipEvidenceSupportRecord
	SupportDecisionEvents   []RelationshipSupportDecisionEvent
	EvidenceFragments       []TraceEvidenceFragment
	EvidenceLifecycleEvents []TraceEvidenceLifecycleEvent
	VerificationEvents      []RelationshipVerificationEvent
	Transitions             []RelationshipTransitionEvent
	Conflicts               []RelationshipConflictCaseRecord
	CrossProfileReferences  []RelationshipCrossReferenceRecord
	IdentityCorrections     []EntityCorrectionEventRecord
	SupersessionLineage     []RelationshipTraceRecord
	SearchDocuments         []TraceSearchDocument
	SemanticNodes           []SemanticGraphNode
	SemanticEdges           []SemanticGraphEdge
	VisitedEntityIDs        []string
	StoppedReason           string
	Truncated               bool
}

type RelationshipTraceRecord struct {
	TeamID             string     `json:"team_id,omitempty"`
	RelationshipID     string     `json:"relationship_id,omitempty"`
	OwnerProfileID     string     `json:"owner_profile_id,omitempty"`
	SpaceID            string     `json:"-"`
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
	Status             string     `json:"status,omitempty"`
	Polarity           string     `json:"polarity,omitempty"`
	ScopeKey           string     `json:"scope_key,omitempty"`
	ValidFrom          *time.Time `json:"valid_from,omitempty"`
	ValidTo            *time.Time `json:"valid_to,omitempty"`
	IdentityAliasOfID  string     `json:"identity_alias_of_relationship_id,omitempty"`
	SupportCount       int        `json:"support_count,omitempty"`
	SourceGroupCount   int        `json:"source_group_count,omitempty"`
	Version            int        `json:"version,omitempty"`
	CreatedAt          time.Time  `json:"created_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at,omitempty"`
	RecordedTo         *time.Time `json:"recorded_to,omitempty"`
}

type RelationshipObservationRecord struct {
	ObservationID     string         `json:"observation_id,omitempty"`
	RelationshipID    string         `json:"relationship_id,omitempty"`
	IngestID          string         `json:"ingest_id,omitempty"`
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

type RelationshipVerificationEvent struct {
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

type RelationshipEvidenceSupportRecord struct {
	SupportID              string         `json:"evidence_support_id,omitempty"`
	RelationshipID         string         `json:"relationship_id,omitempty"`
	ObservationID          string         `json:"observation_id,omitempty"`
	VerificationEventID    string         `json:"verification_event_id,omitempty"`
	FragmentID             string         `json:"evidence_id,omitempty"`
	OwnerProfileID         string         `json:"owner_profile_id,omitempty"`
	EvidenceOwnerProfileID string         `json:"evidence_owner_profile_id,omitempty"`
	SourceGroupKey         string         `json:"source_group_key,omitempty"`
	SourceID               string         `json:"source_id,omitempty"`
	SourceRevisionID       string         `json:"source_revision_id,omitempty"`
	SpanStart              int            `json:"span_start,omitempty"`
	SpanEnd                int            `json:"span_end,omitempty"`
	Quote                  string         `json:"quote,omitempty"`
	Authority              string         `json:"authority,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
	CreatedAt              time.Time      `json:"created_at,omitempty"`
}

type RelationshipSupportDecisionEvent struct {
	SupportDecisionID string         `json:"evidence_support_decision_id,omitempty"`
	SupportID         string         `json:"evidence_support_id,omitempty"`
	RelationshipID    string         `json:"relationship_id,omitempty"`
	OwnerProfileID    string         `json:"owner_profile_id,omitempty"`
	ActorProfileID    string         `json:"actor_profile_id,omitempty"`
	Decision          string         `json:"decision,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"created_at,omitempty"`
}

type ApplyRelationshipSupportDecisionInput struct {
	TeamID         string
	OwnerProfileID string
	RelationshipID string
	SupportID      string
	Decision       string
	Reason         string
	IdempotencyKey string
	Metadata       map[string]any
}

type RelationshipSupportDecisionResult struct {
	TeamID            string
	SupportDecisionID string
	SupportID         string
	RelationshipID    string
	Decision          string
	IdempotencyKey    string
	FromStatus        string
	ToStatus          string
	SupportCount      int
	SourceGroupCount  int
}

type TraceEvidenceFragment struct {
	FragmentID        string         `json:"evidence_id,omitempty"`
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

type TraceEvidenceLifecycleEvent struct {
	LifecycleEventID      string    `json:"lifecycle_event_id,omitempty"`
	LifecycleOperationID  string    `json:"lifecycle_operation_id,omitempty"`
	TargetFragmentID      string    `json:"target_evidence_id,omitempty"`
	ReplacementFragmentID string    `json:"replacement_evidence_id,omitempty"`
	OwnerProfileID        string    `json:"owner_profile_id,omitempty"`
	Action                string    `json:"action,omitempty"`
	Reason                string    `json:"reason,omitempty"`
	CreatedAt             time.Time `json:"created_at,omitempty"`
}

type RelationshipTransitionEvent struct {
	TransitionID        string         `json:"transition_id,omitempty"`
	RelationshipID      string         `json:"relationship_id,omitempty"`
	OwnerProfileID      string         `json:"owner_profile_id,omitempty"`
	FromStatus          string         `json:"from_status,omitempty"`
	ToStatus            string         `json:"to_status,omitempty"`
	Reason              string         `json:"reason,omitempty"`
	VerificationEventID string         `json:"verification_event_id,omitempty"`
	SupportDecisionID   string         `json:"support_decision_id,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at,omitempty"`
}

type RelationshipCrossReferenceRecord struct {
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

type EntityCorrectionEventRecord struct {
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

type TraceSearchDocument struct {
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

type SemanticGraphQuery struct {
	TeamID       string
	Scope        string
	Query        string
	Types        []string
	AnchorType   string
	AnchorID     string
	Depth        int
	Limit        int
	MinRelevance float64
	spaceID      string
}

type SemanticGraphNodeDetailInput struct {
	TeamID   string
	NodeType string
	NodeID   string
}

type SemanticGraphSnapshot struct {
	Scope     string               `json:"scope,omitempty"`
	Query     string               `json:"query,omitempty"`
	Anchor    *SemanticGraphAnchor `json:"anchor,omitempty"`
	Depth     int                  `json:"depth,omitempty"`
	Limit     int                  `json:"limit,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
	Nodes     []SemanticGraphNode  `json:"nodes,omitempty"`
	Edges     []SemanticGraphEdge  `json:"edges,omitempty"`
}

type SemanticGraphAnchor struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Key  string `json:"key,omitempty"`
}

type SemanticGraphNode struct {
	Key            string     `json:"key,omitempty"`
	ID             string     `json:"id,omitempty"`
	Type           string     `json:"type,omitempty"`
	Title          string     `json:"title,omitempty"`
	Body           string     `json:"body,omitempty"`
	Status         string     `json:"status,omitempty"`
	OwnerProfileID string     `json:"owner_profile_id,omitempty"`
	RecordedAt     *time.Time `json:"recorded_at,omitempty"`
}

type SemanticGraphEdge struct {
	ID               string `json:"id,omitempty"`
	RelationshipID   string `json:"relationship_id,omitempty"`
	Source           string `json:"source,omitempty"`
	Target           string `json:"target,omitempty"`
	Relationship     string `json:"relationship,omitempty"`
	Directed         bool   `json:"directed,omitempty"`
	OwnerProfileID   string `json:"owner_profile_id,omitempty"`
	SupportCount     int    `json:"support_count,omitempty"`
	SourceGroupCount int    `json:"source_group_count,omitempty"`
}
