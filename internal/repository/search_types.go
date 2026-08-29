package repository

import (
	"context"
	"time"
)

type SearchRepository interface {
	GetActiveSearchContract(ctx context.Context) (*ActiveSearchContract, error)
	CheckSearchReadiness(ctx context.Context) (*SearchReadiness, error)
	UpsertSearchDocument(ctx context.Context, input UpsertSearchDocumentInput) (*SearchDocumentResult, error)
	ClaimEmbeddingJobs(ctx context.Context, input ClaimEmbeddingJobsInput) ([]EmbeddingJob, error)
	CompleteEmbeddingJob(ctx context.Context, input CompleteEmbeddingJobInput) error
	FailEmbeddingJob(ctx context.Context, input FailEmbeddingJobInput) (*EmbeddingJobFailureResult, error)
	GetEmbeddingQueueStats(ctx context.Context, input EmbeddingQueueStatsInput) (*EmbeddingQueueStats, error)
	SearchFullText(ctx context.Context, input FullTextSearchInput) ([]SearchHit, error)
	SearchExactVector(ctx context.Context, input ExactVectorSearchInput) ([]SearchHit, error)
}

// EmbeddingJobLeaseRenewer is implemented by repositories that can extend a
// claimed embedding job while an external provider call is in flight. It is
// intentionally separate from SearchRepository so lightweight service fakes
// and read-only adapters do not acquire a lease-management obligation.
type EmbeddingJobLeaseRenewer interface {
	RenewEmbeddingJobLease(ctx context.Context, input RenewEmbeddingJobLeaseInput) error
}

// EmbeddingReconciliationRepository is the durable control-plane surface for
// the always-on daily failed-embedding recovery loop. It is separate from the
// tenant-scoped search interface because run leases and operator projections
// are system-coordinated concerns.
type EmbeddingReconciliationRepository interface {
	GetEmbeddingReconciliationTime(ctx context.Context) (time.Time, error)
	GetSearchConvergence(ctx context.Context, input SearchConvergenceInput) (*SearchConvergence, error)
	ReserveEmbeddingReconciliationRun(ctx context.Context, input ReserveEmbeddingReconciliationRunInput) (*EmbeddingReconciliationRun, bool, error)
	SelectEmbeddingReconciliationCanary(ctx context.Context, input SelectEmbeddingReconciliationCanaryInput) (*EmbeddingJob, error)
	MarkEmbeddingReconciliationCanaryAttempt(ctx context.Context, input MarkEmbeddingReconciliationCanaryAttemptInput) error
	CompleteEmbeddingReconciliationCanary(ctx context.Context, input CompleteEmbeddingReconciliationCanaryInput) error
	ResetEmbeddingReconciliationCanary(ctx context.Context, input ResetEmbeddingReconciliationCanaryInput) error
	RequeueEmbeddingReconciliationJobs(ctx context.Context, input RequeueEmbeddingReconciliationJobsInput) (int64, error)
	CompleteEmbeddingReconciliationRun(ctx context.Context, input CompleteEmbeddingReconciliationRunInput) error
}

// SearchRepairRepository owns bounded, document-centric reconciliation. It is
// intentionally separate from the legacy embedding-job surface, which remains
// available to the normal worker until the final cutover.
type SearchRepairRepository interface {
	GetActiveSearchContract(context.Context) (*ActiveSearchContract, error)
	GetSearchRepairTime(context.Context) (time.Time, error)
	ReserveSearchRepairRun(context.Context, SearchRepairRunInput) (*SearchRepairRun, bool, error)
	SelectSearchRepairDocuments(context.Context, SearchRepairSelectionInput) ([]SearchRepairDocument, bool, error)
	CountSearchRepairDocuments(context.Context, SearchRepairSelectionInput) (int64, error)
	ApplySearchRepair(context.Context, ApplySearchRepairInput) (*SearchRepairApplyResult, error)
	FinishSearchRepairRun(context.Context, FinishSearchRepairRunInput) error
}

type RecallRepository interface {
	RecallEvidence(ctx context.Context, input RecallEvidenceInput) (*RecallEvidenceResult, error)
	RecallRelationships(ctx context.Context, input RecallRelationshipsInput) (*RecallRelationshipsResult, error)
}

type ActiveSearchContract struct {
	EmbeddingContractID     string
	SearchIndexGenerationID string
	EmbeddingDimensions     int
	EmbeddingProvider       string
	EmbeddingModel          string
	DistanceMetric          string
	VectorNormalization     string
	DocumentFormatVersion   int
	QueryFormatVersion      int
	IndexGeneration         int
	IndexStrategy           string
	OperatorClass           string
	IndexedExpression       string
	PhysicalIndexName       string
	QueryEFSearch           int
	ExactMaxRows            int
	CandidateLimit          int
	AllowExactFallback      bool
}

type SearchReadiness struct {
	Ready    bool
	Reasons  []SearchReadinessReason
	Contract *ActiveSearchContract
}

type SearchReadinessReason struct {
	Code    string
	Message string
}

type EnsureActiveSearchContractInput struct {
	Provider              string
	Model                 string
	Dimensions            int
	VectorNormalization   string
	DocumentFormatVersion int
	QueryFormatVersion    int
	ExactMaxRows          int
	CandidateLimit        int
}

type EnsureActiveSearchContractResult struct {
	Contract             *ActiveSearchContract
	CreatedContract      bool
	CreatedGeneration    bool
	CreatedPhysicalIndex bool
}

type UpsertSearchDocumentInput struct {
	TeamID                 string
	OwnerProfileID         string
	SourceKind             string
	SourceID               string
	SourceVersion          int64
	ProjectionFormat       int
	ProjectionGenerationID string
	DocumentText           string
	DocumentHash           string
	EmbeddingContractID    string
	Metadata               map[string]any
	SpaceID                string
	SpaceGeneration        int64
	SpaceKind              string
}

type SearchDocumentResult struct {
	TeamID                 string
	SearchDocumentID       string
	OwnerProfileID         string
	SourceKind             string
	SourceID               string
	SourceVersion          int64
	ProjectionFormat       int
	ProjectionGenerationID string
	DocumentVersion        int64
	EmbeddingContractID    string
	EmbeddingDimensions    int
	SearchState            string
	QueuedJobID            string
	SpaceID                string
	SpaceGeneration        int64
}

type ClaimEmbeddingJobsInput struct {
	TeamID   string
	WorkerID string
	Limit    int
	Lease    time.Duration
	SpaceID  string
}

type EmbeddingJob struct {
	TeamID                 string
	EmbeddingJobID         string
	SearchDocumentID       string
	OwnerProfileID         string
	SpaceID                string
	SpaceGeneration        int64
	SourceKind             string
	SourceID               string
	SourceVersion          int64
	ProjectionFormat       int
	ProjectionGenerationID string
	DocumentVersion        int64
	EmbeddingContractID    string
	EmbeddingDimensions    int
	Status                 string
	Attempts               int
	TotalAttempts          int
	RecoveryCount          int
	FailureClass           string
	FailureCode            string
	FirstFailedAt          *time.Time
	LastFailedAt           *time.Time
	LeaseUntil             *time.Time
	DocumentText           string
}

type CompleteEmbeddingJobInput struct {
	TeamID           string
	EmbeddingJobID   string
	WorkerID         string
	ExpectedAttempts int
	Embedding        []float32
	SpaceID          string
}

type RenewEmbeddingJobLeaseInput struct {
	TeamID           string
	EmbeddingJobID   string
	WorkerID         string
	ExpectedAttempts int
	Lease            time.Duration
	SpaceID          string
}

type FailEmbeddingJobInput struct {
	TeamID           string
	EmbeddingJobID   string
	WorkerID         string
	ExpectedAttempts int
	Error            string
	FailureClass     string
	FailureCode      string
	RetryAfter       time.Duration
	Terminal         bool
	SpaceID          string
}

type EmbeddingJobFailureResult struct {
	Status       string
	RetryAfter   time.Duration
	Terminal     bool
	Stale        bool
	Attempts     int
	MaxAttempts  int
	FailureClass string
	FailureCode  string
}

type SearchConvergenceInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
}

type SearchConvergence struct {
	ObservedAt             time.Time
	Status                 string
	Contract               *ActiveSearchContract
	ExpectedDocuments      int64
	CurrentDocuments       int64
	DriftedDocuments       int64
	OldestDriftAge         time.Duration
	DriftClasses           []SearchDocumentDriftCount
	Queued                 int64
	Processing             int64
	Failed                 int64
	ExpiredLeases          int64
	OldestPendingAge       time.Duration
	OldestFailureAge       time.Duration
	QueueAffectedTeamCount int64
	AffectedTeamCount      int64
	Failures               []EmbeddingFailureCount
	FailureGroups          []EmbeddingFailureGroup
	FailureGroupCount      int64
	FailureGroupsTruncated bool
	LatestRun              *EmbeddingReconciliationRun
}

type SearchDocumentDriftCount struct {
	Class string
	Count int64
}

type EmbeddingFailureCount struct {
	SourceKind   string
	FailureClass string
	FailureCode  string
	Count        int64
}

type EmbeddingFailureGroup struct {
	TeamID              string
	TeamName            string
	EmbeddingContractID string
	EmbeddingDimensions int
	SourceKind          string
	FailureClass        string
	FailureCode         string
	Status              string
	FailedJobCount      int64
	QueuedJobCount      int64
	ProcessingJobCount  int64
	AffectedJobCount    int64
	FirstFailedAt       time.Time
	LastFailedAt        time.Time
	Age                 time.Duration
	Guidance            string
}

type EmbeddingReconciliationRun struct {
	RunID               string
	EmbeddingContractID string
	EmbeddingDimensions int
	LocalRunDate        time.Time
	Status              string
	CandidateCutoff     time.Time
	WorkerID            string
	LeaseToken          string
	LeaseUntil          *time.Time
	CanaryJobID         string
	CanaryAttemptedAt   *time.Time
	CanaryOutcome       string
	CanaryFailureClass  string
	CanaryFailureCode   string
	RequeuedCount       int64
	RecoveredCount      int64
	SelectedCount       int64
	EmbeddedCount       int64
	UpdatedCount        int64
	DriftedCount        int64
	LastError           string
	StartedAt           *time.Time
	CompletedAt         *time.Time
	UpdatedAt           time.Time
}

type SearchRepairRun struct {
	RunID               string
	EmbeddingContractID string
	EmbeddingDimensions int
	LocalRunDate        time.Time
	Status              string
	LeaseToken          string
	LeaseUntil          *time.Time
	SelectedCount       int64
	EmbeddedCount       int64
	UpdatedCount        int64
	DriftedCount        int64
	LastError           string
	StartedAt           *time.Time
	CompletedAt         *time.Time
	UpdatedAt           time.Time
}

type SearchRepairRunInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	LocalRunDate        time.Time
	CreateIfMissing     bool
	WorkerID            string
	Lease               time.Duration
}

type SearchRepairSelectionInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	Limit               int
}

// SearchRepairDocument is a snapshot of one canonical document repair. The
// stored fence prevents provider output from replacing a newer document.
type SearchRepairDocument struct {
	TeamID           string
	SearchDocumentID string
	// OwnerProfileID is the canonical owner expected by the source snapshot.
	OwnerProfileID string
	// StoredOwnerProfileID is the owner observed on an existing search document.
	// It is retained as an update fence when canonical ownership has changed.
	StoredOwnerProfileID   string
	SourceKind             string
	SourceID               string
	SourceVersion          int64
	ProjectionFormat       int
	ProjectionGenerationID string
	DocumentVersion        int64
	EmbeddingContractID    string
	EmbeddingDimensions    int
	SearchState            string
	SpaceID                string
	SpaceGeneration        int64
	DocumentText           string
	DocumentHash           string
	StoredDocumentHash     string
	Retired                bool
	ObservedAt             time.Time
}

type SearchRepairEmbedding struct {
	SearchRepairDocument
	Embedding []float32
}

type ApplySearchRepairInput struct {
	RunID                   string
	LeaseToken              string
	EmbeddingContractID     string
	EmbeddingDimensions     int
	SearchIndexGenerationID string
	IndexGeneration         int
	Documents               []SearchRepairEmbedding
}

type SearchRepairApplyResult struct {
	UpdatedCount          int64
	SkippedCount          int64
	RemainingDriftedCount int64
}

type FinishSearchRepairRunInput struct {
	RunID         string
	LeaseToken    string
	Status        string
	SelectedCount int64
	EmbeddedCount int64
	UpdatedCount  int64
	DriftedCount  int64
	LastError     string
}

type ReserveEmbeddingReconciliationRunInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	LocalRunDate        time.Time
	CreateIfMissing     bool
	WorkerID            string
	Lease               time.Duration
}

type SelectEmbeddingReconciliationCanaryInput struct {
	RunID               string
	EmbeddingContractID string
	EmbeddingDimensions int
	CandidateCutoff     time.Time
}

type MarkEmbeddingReconciliationCanaryAttemptInput struct {
	TeamID      string
	RunID       string
	CanaryJobID string
	WorkerID    string
	LeaseToken  string
	AttemptedAt time.Time
	Lease       time.Duration
}

type CompleteEmbeddingReconciliationCanaryInput struct {
	RunID          string
	CanaryJobID    string
	WorkerID       string
	LeaseToken     string
	Succeeded      bool
	RecoveredCount int64
	FailureClass   string
	FailureCode    string
}

type ResetEmbeddingReconciliationCanaryInput struct {
	RunID       string
	CanaryJobID string
	WorkerID    string
	LeaseToken  string
}

type RequeueEmbeddingReconciliationJobsInput struct {
	RunID               string
	WorkerID            string
	LeaseToken          string
	EmbeddingContractID string
	EmbeddingDimensions int
	CandidateCutoff     time.Time
	BatchSize           int
	Lease               time.Duration
}

type CompleteEmbeddingReconciliationRunInput struct {
	RunID          string
	WorkerID       string
	LeaseToken     string
	Status         string
	CanaryOutcome  string
	FailureClass   string
	FailureCode    string
	RequeuedCount  int64
	RecoveredCount int64
	LastError      string
}

type EmbeddingQueueStatsInput struct {
	TeamID              string
	EmbeddingContractID string
	EmbeddingDimensions int
	ActiveTeamsOnly     bool
}

type EmbeddingQueueStats struct {
	TeamID              string
	EmbeddingContractID string
	EmbeddingDimensions int
	Queued              int64
	Processing          int64
	Completed           int64
	Failed              int64
	Stale               int64
	Cancelled           int64
	ExpiredLeases       int64
	OldestPendingAge    time.Duration
	OldestLeaseAge      time.Duration
	TerminalFailures    int64
	CutoverBlocking     bool
}

type FullTextSearchInput struct {
	TeamID     string
	Query      string
	SourceKind string
	Limit      int
}

type ExactVectorSearchInput struct {
	TeamID              string
	EmbeddingContractID string
	SourceKind          string
	QueryEmbedding      []float32
	Limit               int
}

type SearchHit struct {
	TeamID              string
	SearchDocumentID    string
	SourceKind          string
	SourceID            string
	SourceVersion       int64
	DocumentVersion     int64
	EmbeddingContractID string
	SearchState         string
	Distance            float64
	TextRank            float64
}

type RecallEvidenceInput struct {
	TeamID               string
	Query                string
	QueryEmbedding       []float32
	Limit                int
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	SpaceID              string
	SpaceKind            string
}

type RecallRelationshipsInput struct {
	TeamID               string
	Query                string
	QueryEmbedding       []float32
	Limit                int
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownEvidenceIDs     []string
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
	ExcludedGroupKeys    []string
	SpaceID              string
	SpaceKind            string
}

type RecallEvidenceResult struct {
	TeamID      string
	SearchState string
	Results     []RecallEvidenceHit
	Conflicts   []RelationshipConflictCaseRecord
}

type RecallRelationshipsResult struct {
	TeamID        string
	SearchState   string
	VectorOmitted bool
	Results       []RecallRelationshipHit
}

type RecallEvidenceHit struct {
	TeamID          string
	EvidenceID      string
	RelationshipIDs []string
	Context         string
	Source          string
	SourceType      string
	CreatedAt       time.Time
	Rank            int
	Score           float64
	SearchState     string
	SpaceKind       string
}

type RecallRelationshipHit struct {
	TeamID                    string
	RelationshipID            string
	SemanticGroupKey          string
	SubjectEntityID           string
	SubjectName               string
	PredicateKey              string
	ObjectEntityID            string
	ObjectValueID             string
	ObjectName                string
	ObjectValueType           string
	ObjectValue               string
	Polarity                  string
	ScopeKey                  string
	ValidFrom                 *time.Time
	Score                     float64
	Rank                      int
	SearchState               string
	SupportCount              int
	SourceGroupCount          int
	EvidenceIDs               []string
	EquivalentRelationshipIDs []string
	CreatedAt                 time.Time
	SpaceKind                 string
}
