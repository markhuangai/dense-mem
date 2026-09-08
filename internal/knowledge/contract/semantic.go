package contract

import "time"

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
	FragmentID               string
	OccurrenceID             string
	OccurrenceOwnerProfileID string
	EvidenceOwnerProfileID   string
	SourceGroupKey           string
	SourceID                 string
	SourceRevisionID         string
	SpanStart                int
	SpanEnd                  int
	Quote                    string
	Authority                string
	Metadata                 map[string]any
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
