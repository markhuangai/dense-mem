package repository

import "time"

type CommitSemanticInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	FragmentID     string

	EntityResolutions        []SemanticEntityResolutionInput
	RelationshipObservations []SemanticRelationshipDecisionInput
}

type SemanticEntityResolutionInput struct {
	MentionRef      string
	Action          string
	EntityID        string
	ExactEntityID   string
	EntityKind      string
	CanonicalName   string
	FragmentID      string
	SpanStart       *int
	SpanEnd         *int
	IdentityContext map[string]any
	VerifierResult  map[string]any
	Metadata        map[string]any
	AssessmentID    string
}

type SemanticRelationshipDecisionInput struct {
	Ref                string
	SubjectRef         string
	OriginalPredicate  string
	PredicateKey       string
	PredicateVersion   int
	ExactPredicateKey  string
	PredicateCandidate *SemanticPredicateCandidateInput
	ObjectRef          string
	ObjectValue        *SemanticValueInput
	Polarity           string
	ScopeKey           string
	ValidFrom          *time.Time
	ValidTo            *time.Time
	EvidenceVerdict    string
	// AssessorAccepted marks the v2.6 Remember structural-assessment path.
	// It carries support spans without manufacturing a legacy verdict or
	// confidence policy value for the shared semantic tables.
	AssessorAccepted        bool
	PromoteToFact           bool
	Confidence              *float64
	Rationale               string
	Model                   string
	ResponseHash            string
	Support                 *EvidenceSupportInput
	Supports                []EvidenceSupportInput
	CorrectionTarget        *SemanticCorrectionTargetInput
	ConflictContext         *SemanticConflictContextInput
	ObservationMetadata     map[string]any
	RelationshipMetadata    map[string]any
	AssessmentID            string
	AssessmentPolicyVersion string
	ThresholdUsed           *float64
	GateResult              string
	SuppressSupport         bool
}

type SemanticPredicateCandidateInput struct {
	PredicateKey     string
	PredicateVersion int
	RelationshipKind string
}

type SemanticCorrectionTargetInput struct {
	RelationshipID  string
	ExpectedVersion int
}

type SemanticConflictContextInput struct {
	ConflictID      string
	ExpectedVersion int
}

type SemanticValueInput struct {
	Ref                  string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Metadata             map[string]any
}

type submissionSemanticCommitState struct {
	Status              string
	OutcomeID           string
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}
