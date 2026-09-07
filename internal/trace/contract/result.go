package contract

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

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
	SemanticNodes           []graphcontract.Node
	SemanticEdges           []graphcontract.Edge
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
	OccurrenceID           string         `json:"occurrence_id,omitempty"`
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

type TraceEvidenceFragment struct {
	FragmentID        string         `json:"evidence_id,omitempty"`
	OccurrenceID      string         `json:"occurrence_id,omitempty"`
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

type RelationshipConflictCaseRecord struct {
	TeamID              string
	ConflictID          string
	SpaceID             string `json:"-"`
	SemanticScopeKey    string
	Kind                string
	Status              string
	SubjectEntityID     string
	PredicateKey        string
	PredicateVersion    int
	RelationshipKind    string
	CurrentCardinality  string
	Polarity            string
	ScopeKey            string
	Question            string
	PolicyVersion       string
	ReviewDueAt         time.Time
	NextReviewAt        time.Time
	ReviewTTLDays       int
	Timezone            string
	PreferredPositionID string
	ResolvedAt          *time.Time
	EffectiveAt         *time.Time
	EffectiveTimeBasis  string
	ResolutionReason    string
	Version             int
	Attempts            int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Positions           []domain.RelationshipConflictPositionRecord
	DismissedAt         *time.Time
}
