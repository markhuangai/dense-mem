package domain

import "time"

type SemanticEntityKind string

const (
	SemanticEntityUnknown      SemanticEntityKind = "unknown"
	SemanticEntityPerson       SemanticEntityKind = "person"
	SemanticEntityOrganization SemanticEntityKind = "organization"
	SemanticEntityProject      SemanticEntityKind = "project"
	SemanticEntityProduct      SemanticEntityKind = "product"
	SemanticEntityPlace        SemanticEntityKind = "place"
	SemanticEntityDocument     SemanticEntityKind = "document"
	SemanticEntityConcept      SemanticEntityKind = "concept"
)

func (k SemanticEntityKind) IsValid() bool {
	switch k {
	case SemanticEntityUnknown, SemanticEntityPerson, SemanticEntityOrganization,
		SemanticEntityProject, SemanticEntityProduct, SemanticEntityPlace,
		SemanticEntityDocument, SemanticEntityConcept:
		return true
	default:
		return false
	}
}

type SemanticRelationshipTier string

const (
	SemanticTierCandidate      SemanticRelationshipTier = "candidate"
	SemanticTierValidatedClaim SemanticRelationshipTier = "validated_claim"
	SemanticTierFact           SemanticRelationshipTier = "fact"
)

func (t SemanticRelationshipTier) IsValid() bool {
	switch t {
	case SemanticTierCandidate, SemanticTierValidatedClaim, SemanticTierFact:
		return true
	default:
		return false
	}
}

type SemanticRelationshipStatus string

const (
	SemanticStatusPendingEvidence SemanticRelationshipStatus = "pending_evidence"
	SemanticStatusActive          SemanticRelationshipStatus = "active"
	SemanticStatusNeedsReview     SemanticRelationshipStatus = "needs_review"
	SemanticStatusQuarantined     SemanticRelationshipStatus = "quarantined"
	SemanticStatusDisputed        SemanticRelationshipStatus = "disputed"
	SemanticStatusRejected        SemanticRelationshipStatus = "rejected"
	SemanticStatusRetracted       SemanticRelationshipStatus = "retracted"
	SemanticStatusSuperseded      SemanticRelationshipStatus = "superseded"
)

func (s SemanticRelationshipStatus) IsValid() bool {
	switch s {
	case SemanticStatusPendingEvidence, SemanticStatusActive, SemanticStatusNeedsReview,
		SemanticStatusQuarantined, SemanticStatusDisputed, SemanticStatusRejected,
		SemanticStatusRetracted, SemanticStatusSuperseded:
		return true
	default:
		return false
	}
}

type SemanticHypothesisStatus string

const (
	SemanticHypothesisProposed   SemanticHypothesisStatus = "proposed"
	SemanticHypothesisSubmitted  SemanticHypothesisStatus = "submitted"
	SemanticHypothesisRejected   SemanticHypothesisStatus = "rejected"
	SemanticHypothesisStale      SemanticHypothesisStatus = "stale"
	SemanticHypothesisReinforced SemanticHypothesisStatus = "reinforced"
)

func (s SemanticHypothesisStatus) IsValid() bool {
	switch s {
	case SemanticHypothesisProposed, SemanticHypothesisSubmitted, SemanticHypothesisRejected,
		SemanticHypothesisStale, SemanticHypothesisReinforced:
		return true
	default:
		return false
	}
}

type SemanticEvidenceFragment struct {
	TeamID            string         `json:"team_id"`
	FragmentID        string         `json:"fragment_id"`
	OwnerProfileID    string         `json:"owner_profile_id"`
	OwnerProfileName  string         `json:"owner_profile_name,omitempty"`
	Content           string         `json:"content"`
	Source            string         `json:"source,omitempty"`
	SourceDocID       string         `json:"source_doc_id,omitempty"`
	SourceGroup       string         `json:"source_group,omitempty"`
	SourceType        SourceType     `json:"source_type"`
	Authority         Authority      `json:"authority,omitempty"`
	Labels            []string       `json:"labels,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	ContentHash       string         `json:"content_hash"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	EmbeddingModel    string         `json:"embedding_model,omitempty"`
	EmbeddingDims     int            `json:"embedding_dimensions,omitempty"`
	EmbeddingContract string         `json:"embedding_contract_id,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

type SemanticEntity struct {
	TeamID         string             `json:"team_id"`
	EntityID       string             `json:"entity_id"`
	OwnerProfileID string             `json:"owner_profile_id"`
	Kind           SemanticEntityKind `json:"kind"`
	CanonicalName  string             `json:"canonical_name"`
	Metadata       map[string]any     `json:"metadata,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type SemanticRelationship struct {
	TeamID                       string                     `json:"team_id"`
	RelationshipID               string                     `json:"relationship_id"`
	OwnerProfileID               string                     `json:"owner_profile_id"`
	OwnerProfileName             string                     `json:"owner_profile_name,omitempty"`
	SubjectEntityID              string                     `json:"subject_entity_id"`
	SubjectEntityName            string                     `json:"subject_entity_name,omitempty"`
	SubjectEntityKind            SemanticEntityKind         `json:"subject_entity_kind,omitempty"`
	Predicate                    string                     `json:"predicate"`
	Polarity                     ClaimPolarity              `json:"polarity,omitempty"`
	ObjectEntityID               string                     `json:"object_entity_id,omitempty"`
	ObjectEntityName             string                     `json:"object_entity_name,omitempty"`
	ObjectEntityKind             SemanticEntityKind         `json:"object_entity_kind,omitempty"`
	ObjectValue                  string                     `json:"object_value,omitempty"`
	ObjectKind                   string                     `json:"object_kind,omitempty"`
	Tier                         SemanticRelationshipTier   `json:"tier"`
	Status                       SemanticRelationshipStatus `json:"status"`
	Confidence                   float64                    `json:"confidence"`
	SupportCount                 int                        `json:"support_count,omitempty"`
	SourceGroupCount             int                        `json:"source_group_count,omitempty"`
	SemanticGroupKey             string                     `json:"semantic_group_key,omitempty"`
	PrimarySourceGroup           string                     `json:"primary_source_group,omitempty"`
	Version                      int64                      `json:"version,omitempty"`
	CorroboratingRelationshipIDs []string                   `json:"corroborating_relationship_ids,omitempty"`
	ConflictingRelationshipIDs   []string                   `json:"conflicting_relationship_ids,omitempty"`
	ValidFrom                    *time.Time                 `json:"valid_from,omitempty"`
	ValidTo                      *time.Time                 `json:"valid_to,omitempty"`
	RecordedAt                   time.Time                  `json:"recorded_at"`
	RecordedTo                   *time.Time                 `json:"recorded_to,omitempty"`
	CreatedAt                    time.Time                  `json:"created_at"`
	UpdatedAt                    time.Time                  `json:"updated_at"`
}

type SemanticFrontierHint struct {
	ViaRelationshipID string                   `json:"via_relationship_id"`
	RelationshipID    string                   `json:"relationship_id"`
	Direction         string                   `json:"direction"`
	Predicate         string                   `json:"predicate"`
	NeighborEntityID  string                   `json:"neighbor_entity_id,omitempty"`
	NeighborName      string                   `json:"neighbor_name,omitempty"`
	NeighborKind      SemanticEntityKind       `json:"neighbor_kind,omitempty"`
	NeighborValue     string                   `json:"neighbor_value,omitempty"`
	NeighborValueKind string                   `json:"neighbor_value_kind,omitempty"`
	Tier              SemanticRelationshipTier `json:"tier"`
	ValidFrom         *time.Time               `json:"valid_from,omitempty"`
	ValidTo           *time.Time               `json:"valid_to,omitempty"`
}

type SemanticRelationshipSupport struct {
	TeamID         string    `json:"team_id"`
	RelationshipID string    `json:"relationship_id"`
	FragmentID     string    `json:"fragment_id"`
	EvidenceIndex  int       `json:"evidence_index"`
	Quote          string    `json:"quote,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type SemanticRecallResult struct {
	Relationship          *SemanticRelationship         `json:"relationship,omitempty"`
	Relationships         []SemanticRelationship        `json:"relationships,omitempty"`
	Evidence              *SemanticEvidenceFragment     `json:"evidence,omitempty"`
	Evidences             []SemanticEvidenceFragment    `json:"evidences,omitempty"`
	Supports              []SemanticRelationshipSupport `json:"supports,omitempty"`
	Frontier              []SemanticFrontierHint        `json:"frontier,omitempty"`
	MatchedExactEntityIDs []string                      `json:"-"`
	Score                 float64                       `json:"score"`
	Rank                  int                           `json:"rank"`
}

type SemanticRecallBranch string

const (
	SemanticRecallBranchExact              SemanticRecallBranch = "exact"
	SemanticRecallBranchEvidenceText       SemanticRecallBranch = "evidence_text"
	SemanticRecallBranchRelationshipText   SemanticRecallBranch = "relationship_text"
	SemanticRecallBranchEvidenceVector     SemanticRecallBranch = "evidence_vector"
	SemanticRecallBranchRelationshipVector SemanticRecallBranch = "relationship_vector"
	SemanticRecallBranchAdjacency          SemanticRecallBranch = "adjacency"
)

func (b SemanticRecallBranch) IsValid() bool {
	switch b {
	case SemanticRecallBranchExact,
		SemanticRecallBranchEvidenceText,
		SemanticRecallBranchRelationshipText,
		SemanticRecallBranchEvidenceVector,
		SemanticRecallBranchRelationshipVector,
		SemanticRecallBranchAdjacency:
		return true
	default:
		return false
	}
}

type SemanticRecallQueryFeatures struct {
	Query             string
	ContentQuery      string
	RelaxedQuery      string
	Terms             []string
	EntityPhrases     []string
	HardAnchors       []string
	TemporalIntent    bool
	CurrentnessIntent bool
}

type SemanticRecallSearchScope struct {
	TeamID                   string
	Features                 SemanticRecallQueryFeatures
	Embedding                []float32
	EmbeddingContractID      string
	KnownEvidenceIDs         []string
	KnownRelationshipIDs     []string
	ExpandFromEntityIDs      []string
	ValidAt                  time.Time
	KnownAt                  time.Time
	BranchLimit              int
	RelationshipsPerEvidence int
}

type SemanticRecallCandidate struct {
	EvidenceID              string
	Branch                  SemanticRecallBranch
	Rank                    int
	RawScore                float64
	ExactMatch              bool
	PreciseMatch            bool
	PhraseMatch             bool
	AllHardAnchorsMatched   bool
	FactSupport             bool
	IndependentSourceGroups int
	LatestValidFrom         *time.Time
	LatestRecordedAt        time.Time
	RelationshipIDs         []string
	MatchedEntityIDs        []string
}

type SemanticRecallEntitySeed struct {
	EntityID   string
	Rank       int
	Exact      bool
	HardAnchor bool
	Explicit   bool
	Score      float64
}

type SemanticRecallCandidateBatch struct {
	Candidates  []SemanticRecallCandidate
	EntitySeeds []SemanticRecallEntitySeed
}

type SemanticTraceResult struct {
	Relationship *SemanticRelationship         `json:"relationship,omitempty"`
	Supports     []SemanticRelationshipSupport `json:"supports,omitempty"`
	Evidence     []SemanticEvidenceFragment    `json:"evidence,omitempty"`
}
