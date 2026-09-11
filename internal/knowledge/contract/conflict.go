package contract

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

const ConflictAssessmentMaxFailedDays = 5

type ConflictRuntimeConfig struct {
	ReviewTTLDays int
	Timezone      string
}

type ConflictReviewRunInput struct {
	TeamID       string
	WorkerID     string
	LocalRunDate time.Time
	Timezone     string
	Lease        time.Duration
}

type ConflictReviewRunCompleteInput struct {
	TeamID        string
	ReviewRunID   string
	WorkerID      string
	Status        string
	ClaimedCases  int
	ResolvedCases int
	OverdueCases  int
	NoOpCases     int
	FailedCases   int
	LastError     string
}

type ConflictReviewRunRecord struct {
	TeamID       string
	ReviewRunID  string
	LocalRunDate time.Time
	Status       string
	WorkerID     string
}

type ClaimRelationshipConflictCasesInput struct {
	TeamID              string
	WorkerID            string
	ReviewRunID         string
	Limit               int
	Lease               time.Duration
	MaxAttempts         int
	Now                 time.Time
	ExcludedConflictIDs []string
}

// ReleaseRelationshipConflictCaseClaimInput identifies the worker claim that
// should be returned to the conflict queue after retryable processing fails.
type ReleaseRelationshipConflictCaseClaimInput struct {
	TeamID      string
	ConflictID  string
	WorkerID    string
	ReviewRunID string
	Now         time.Time
}

type ReviewRelationshipConflictCaseInput struct {
	TeamID      string
	WorkerID    string
	ReviewRunID string
	ConflictID  string
	Now         time.Time
}

type ReviewRelationshipConflictCaseResult struct {
	ConflictID           string
	Outcome              string
	Stage                string
	PreferredPositionID  string
	Resolution           *RelationshipConflictResolutionInput
	UpdatedRelationships []string
	RetractedEvidenceIDs []string
	AssessmentAttemptID  string
	ResolutionMethod     string
	ResolutionPending    bool
}

// RelationshipConflictResolutionInput identifies one selected resolution. The
// repository revalidates every field before it mutates durable state.
type RelationshipConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	ExpectedCaseVersion int
	PreferredPositionID string
	AssessmentAttemptID string
	Method              string
	Now                 time.Time
}

type RelationshipConflictResolutionDocument struct {
	TeamID          string
	RelationshipID  string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
	SourceVersion   int64
	DocumentHash    string
	DocumentText    string
}

type RelationshipConflictResolutionFence struct {
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type RelationshipConflictResolutionPlan struct {
	Resolution          RelationshipConflictResolutionInput
	Fence               RelationshipConflictResolutionFence
	Documents           []RelationshipConflictResolutionDocument
	EffectiveAt         time.Time
	EffectiveTimeBasis  string
	Reason              string
	ResolutionPlanID    string
	Pending             bool
	PendingTransitioned bool
	Stale               bool
}

type RelationshipConflictResolutionEmbedding struct {
	DocumentHash string
	Embedding    []float32
}

type CommitRelationshipConflictResolutionInput struct {
	Plan       RelationshipConflictResolutionPlan
	Embeddings []RelationshipConflictResolutionEmbedding
}

type RelationshipConflictCaseRecord = tracecontract.RelationshipConflictCaseRecord

type ValidateRelationshipConflictContextInput struct {
	TeamID          string
	OwnerProfileID  string
	ConflictID      string
	ExpectedVersion int
}

type ReserveOverdueConflictAssessmentInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	LocalAssessmentDate time.Time
	Model               string
	PolicyVersion       string
}

type OverdueConflictAssessmentReservation struct {
	AssessmentAttemptID string
	CaseVersion         int
	Model               string
	PolicyVersion       string
	LastWriteWins       bool
}

type OverdueConflictAssessmentDossier struct {
	TeamID      string
	ConflictID  string
	CaseVersion int
	Question    string
	Positions   []OverdueConflictAssessmentPosition
	Evidence    []OverdueConflictAssessmentEvidence
}

type OverdueConflictAssessmentPosition struct {
	PositionID     string
	PositionKey    string
	SupporterCount int
	Supports       []domain.ConflictResolutionSupport
}

type OverdueConflictAssessmentEvidence struct {
	FragmentID     string
	OwnerProfileID string
	SupporterRef   string
	PositionID     string
	SupportID      string
	Authority      string
	AcceptedAt     time.Time
	EffectiveAt    *time.Time
	EvidenceIndex  int
	Content        string
}

type CompleteOverdueConflictAssessmentInput struct {
	TeamID              string
	ConflictID          string
	AssessmentAttemptID string
	CaseVersion         int
	ReviewRunID         string
	Decision            string
	SelectedPositionID  string
	Confidence          *float64
	ProviderTurns       int
	ResponseHash        string
	FailureClass        string
}

type CompleteOverdueConflictAssessmentResult struct {
	FailureCount int
}

type ApplyOverdueConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	ExpectedCaseVersion int
	PreferredPositionID string
	AssessmentAttemptID string
	Method              string
	Now                 time.Time
}

type ApplyOverdueConflictResolutionResult struct {
	ConflictID           string
	PreferredPositionID  string
	Method               string
	Resolved             bool
	Pending              bool
	PendingTransitioned  bool
	Stale                bool
	UpdatedRelationships []string
	RetractedEvidenceIDs []string
	DerivedEvidence      []ConflictDerivedEvidenceTarget
}

type ResumePendingOverdueConflictResolutionInput struct {
	TeamID      string
	ConflictID  string
	ReviewRunID string
	WorkerID    string
	Now         time.Time
}

type ConflictDerivedEvidenceTarget struct {
	TaskID               string
	TeamID               string
	SpaceID              string
	SpaceGeneration      int64
	ConflictID           string
	SystemProfileID      string
	TargetFragmentID     string
	TargetOwnerProfileID string
	SelectedPositionID   string
	SourceGroupKey       string
	EvidenceIndex        int
}

type ClaimConflictDerivedEvidenceTasksInput struct {
	TeamID      string
	ReviewRunID string
	WorkerID    string
	Limit       int
	Lease       time.Duration
}

type StageConflictDerivedEvidenceResult struct {
	IngestID            string
	ReplacementFragment string
	Existing            bool
}
