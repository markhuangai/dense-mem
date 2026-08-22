package repository

import "time"

type CommitPlacementSemanticInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	OutcomeKind      string
	Status           string
	Category         string
	Payload          map[string]any
	RetryAfter       time.Duration

	EntityResolutions        []PlacementEntityResolutionInput
	RelationshipObservations []PlacementRelationshipDecisionInput
	RelationshipDecisions    []ApplyRelationshipDecisionInput
}

type PlacementEntityResolutionInput struct {
	MentionRef      string
	Action          string
	EntityID        string
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

type PlacementRelationshipDecisionInput struct {
	Ref                     string
	SubjectRef              string
	OriginalPredicate       string
	PredicateKey            string
	PredicateVersion        int
	PredicateCandidate      *PlacementPredicateCandidateInput
	ObjectRef               string
	ObjectValue             *PlacementValueInput
	Polarity                string
	ScopeKey                string
	ValidFrom               *time.Time
	ValidTo                 *time.Time
	EvidenceVerdict         string
	PromoteToFact           bool
	Confidence              *float64
	Rationale               string
	Model                   string
	ResponseHash            string
	Support                 *EvidenceSupportInput
	Supports                []EvidenceSupportInput
	CorrectionTarget        *PlacementCorrectionTargetInput
	ConflictContext         *PlacementConflictContextInput
	ObservationMetadata     map[string]any
	RelationshipMetadata    map[string]any
	AssessmentID            string
	AssessmentPolicyVersion string
	ThresholdUsed           *float64
	GateResult              string
	SuppressSupport         bool
}

type PlacementPredicateCandidateInput struct {
	PredicateKey     string
	PredicateVersion int
	RelationshipKind string
}

type PlacementCorrectionTargetInput struct {
	RelationshipID  string
	ExpectedVersion int
}

type PlacementConflictContextInput struct {
	ConflictID      string
	ExpectedVersion int
}

type PlacementValueInput struct {
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
	FirstDisposition    *PlacementFirstDisposition
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}

// PlacementFirstDisposition is produced once, in the transaction that first
// moves a placement run to a terminal disposition.
type PlacementFirstDisposition struct {
	Status      string
	CreatedAt   time.Time
	CompletedAt time.Time
	IsRemember  bool
}
