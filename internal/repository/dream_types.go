package repository

import (
	"context"
	"time"
)

type DreamRepository interface {
	ClaimDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
	CompleteDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error
	ListDreamInputs(ctx context.Context, input DreamInputListInput) ([]DreamInput, error)
	ListDreamTargetPredicates(ctx context.Context, teamID string) ([]DreamTargetPredicate, error)
	ListAvailableDreamTargets(ctx context.Context, teamID string, targets []DreamTargetCandidate) ([]DreamTargetCandidate, error)
	ListUnassessedDreamPaths(ctx context.Context, teamID string, paths []DreamPathEvaluationInput) ([]DreamPathEvaluationInput, error)
	RecordDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error
	PersistDreamGeneration(ctx context.Context, input DreamGenerationPersistInput) (DreamGenerationPersistResult, error)
	UpsertHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error)
	ListHypotheses(ctx context.Context, input ListHypothesesInput) ([]HypothesisRecord, string, error)
	GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error)
	RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error)
	UpdateHypothesisStatus(ctx context.Context, input UpdateHypothesisStatusInput) (*HypothesisRecord, error)
	SubmitHypothesis(ctx context.Context, input SubmitHypothesisInput) (*HypothesisRecord, error)
	CountHypotheses(ctx context.Context, teamID, status string) (int, error)
	ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error)
}

// ScheduledDreamRepository is deliberately separate from DreamRepository's
// authenticated actor methods. Only the scheduler receives this system-mode
// port, so a request cannot select system mutation mode.
type ScheduledDreamRepository interface {
	ClaimScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
	ClaimRecoverableScheduledDreamCycle(ctx context.Context, input DreamCycleRecoveryClaimInput) (*DreamCycleRun, error)
	CompleteScheduledDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error
	UpsertScheduledHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error)
	RecordScheduledDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error
	PersistScheduledDreamGeneration(ctx context.Context, input DreamGenerationPersistInput) (DreamGenerationPersistResult, error)
	RecordMissedScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error)
}

type DreamCycleClaimInput struct {
	TeamID               string
	InitiatedByProfileID string
	RunDate              string
	WindowKey            string
	ScheduledFor         *time.Time
	LeaseToken           string
	LeaseUntil           time.Time
	SourceSnapshot       []map[string]any
}

type DreamCycleRecoveryClaimInput struct {
	TeamID      string
	LeaseToken  string
	LeaseUntil  time.Time
	MaxAttempts int
}

type DreamCycleCompleteInput struct {
	TeamID               string
	InitiatedByProfileID string
	RunID                string
	LeaseToken           string
	Status               string
	InputCount           int
	CreatedHypotheses    int
	RejectedHypotheses   int
	SourceSnapshot       []map[string]any
	ProviderModel        string
	ProviderTurns        int
	ProviderInputTokens  int
	ProviderOutputTokens int
	AttemptedPaths       int
	ProviderProposals    int
	OutcomeSummary       map[string]int
	Error                string
}

type DreamCycleRun struct {
	TeamID               string
	RunID                string
	InitiatedByProfileID string
	RunDate              string
	WindowKey            string
	Status               string
	ScheduledFor         *time.Time
	LeaseToken           string
	LeaseUntil           *time.Time
	AttemptCount         int
	InputCount           int
	CreatedHypotheses    int
	RejectedHypotheses   int
	ProviderModel        string
	ProviderTurns        int
	ProviderInputTokens  int
	ProviderOutputTokens int
	AttemptedPaths       int
	ProviderProposals    int
	OutcomeSummary       map[string]int
	Error                string
	StartedAt            time.Time
	CompletedAt          *time.Time
	Claimed              bool
}

type DreamInputListInput struct {
	TeamID string
	Limit  int
}

type DreamInput struct {
	RelationshipID     string
	OwnerProfileID     string
	Version            int
	Status             string
	SubjectEntityID    string
	SubjectName        string
	PredicateKey       string
	PredicateVersion   int
	ObjectEntityID     string
	ObjectValueID      string
	ObjectName         string
	RelationshipKind   string
	CurrentCardinality string
	SubjectKind        string
	ObjectKind         string
	Evidence           []DreamEvidence
}

// DreamEvidence identifies an exact server-selected source excerpt. Its
// content is sent to the provider as untrusted data only after eligibility is
// established inside PostgreSQL.
type DreamEvidence struct {
	EvidenceRef      string
	SupportID        string
	ObservationID    string
	FragmentID       string
	SourceID         string
	SourceRevisionID string
	SourceGroupKey   string
	Authority        string
	SpanStart        int
	SpanEnd          int
	Content          string
}

type DreamTargetPredicate struct {
	PredicateRef        string
	PredicateKey        string
	Version             int
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
}

type DreamTargetCandidate struct {
	PathRef         string
	PredicateRef    string
	SubjectEntityID string
	PredicateKey    string
	ObjectEntityID  string
	ObjectValueID   string
}

type DreamPathEvaluationInput struct {
	FirstRelationshipID         string
	FirstRelationshipVersion    int
	SecondRelationshipID        string
	SecondRelationshipVersion   int
	AllowedPredicateFingerprint string
}

type DreamPathEvaluationRecordInput struct {
	TeamID             string
	CreatedByProfileID string
	ProviderModel      string
	Paths              []DreamPathEvaluationInput
}

// DreamGenerationPersistInput is the durable result of one already validated
// provider response. Proposals and path assessments commit together.
type DreamGenerationPersistInput struct {
	TeamID             string
	CreatedByProfileID string
	RunID              string
	LeaseToken         string
	ProviderModel      string
	Proposals          []UpsertHypothesisInput
	EvaluatedPaths     []DreamPathEvaluationInput
}

type DreamGenerationPersistResult struct {
	Created  int
	Rejected int
}

type UpsertHypothesisInput struct {
	TeamID                string
	CreatedByProfileID    string
	RunID                 string
	Statement             string
	Rationale             string
	Likelihood            *float64
	Confidence            *float64
	SubjectEntityID       string
	PredicateKey          string
	PredicateVersion      int
	ObjectEntityID        string
	ObjectValueID         string
	SourceRefs            []map[string]any
	SourceVersions        map[string]int
	SourceOwnerProfileIDs []string
	ContentHash           string
	TargetIdentity        string
	Derivations           []DreamDerivationSource
	GeneratorKind         string
	GeneratorVersion      string
	Payload               map[string]any
}

type DreamDerivationSource struct {
	PremisePosition     int
	RelationshipID      string
	RelationshipVersion int
	SupportID           string
	ObservationID       string
	FragmentID          string
	SourceID            string
	SourceRevisionID    string
	SourceGroupKey      string
	SpanStart           int
	SpanEnd             int
	Quote               string
	Authority           string
}

type HypothesisRecord struct {
	TeamID                string
	HypothesisID          string
	CreatedByProfileID    string
	Status                string
	Statement             string
	Rationale             string
	Likelihood            *float64
	Confidence            *float64
	SubjectEntityID       string
	PredicateKey          string
	PredicateVersion      int
	ObjectEntityID        string
	ObjectValueID         string
	SourceRefs            []map[string]any
	SourceVersions        map[string]int
	SourceOwnerProfileIDs []string
	ContentHash           string
	TargetIdentity        string
	Derivations           []DreamDerivationSource
	CycleRunID            string
	GeneratorKind         string
	GeneratorVersion      string
	InvalidatedReason     string
	SubmittedIngestID     string
	SubmittedSubmissionID string
	SubmittedAt           *time.Time
	Payload               map[string]any
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ListHypothesesInput struct {
	TeamID    string
	Status    string
	Limit     int
	Cursor    string
	Sort      string
	Direction string
}

type GetHypothesisInput struct {
	TeamID       string
	HypothesisID string
}

type RecallHypothesesInput struct {
	TeamID string
	Query  string
	Limit  int
}

type UpdateHypothesisStatusInput struct {
	TeamID            string
	ActorProfileID    string
	HypothesisID      string
	Status            string
	Decision          string
	InvalidatedReason string
}

type SubmitHypothesisInput struct {
	TeamID                string
	ActorProfileID        string
	HypothesisID          string
	Decision              string
	SubmittedSubmissionID string
	InvalidatedReason     string
}

var _ DreamRepository = (*SemanticRepositoryImpl)(nil)
var _ ScheduledDreamRepository = (*SemanticRepositoryImpl)(nil)
