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
}

type ClaimEmbeddingJobsInput struct {
	TeamID   string
	WorkerID string
	Limit    int
	Lease    time.Duration
}

type EmbeddingJob struct {
	TeamID                 string
	EmbeddingJobID         string
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
	Status                 string
	Attempts               int
	LeaseUntil             *time.Time
	DocumentText           string
}

type CompleteEmbeddingJobInput struct {
	TeamID           string
	EmbeddingJobID   string
	WorkerID         string
	ExpectedAttempts int
	Embedding        []float32
}

type FailEmbeddingJobInput struct {
	TeamID           string
	EmbeddingJobID   string
	WorkerID         string
	ExpectedAttempts int
	Error            string
	RetryAfter       time.Duration
	Terminal         bool
}

type EmbeddingJobFailureResult struct {
	Status      string
	RetryAfter  time.Duration
	Terminal    bool
	Stale       bool
	Attempts    int
	MaxAttempts int
}

type EmbeddingQueueStatsInput struct {
	TeamID              string
	EmbeddingContractID string
	EmbeddingDimensions int
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
}

type RecallRelationshipsInput struct {
	TeamID               string
	Query                string
	QueryEmbedding       []float32
	Limit                int
	ValidAt              *time.Time
	KnownAt              *time.Time
	KnownRelationshipIDs []string
	ExpandFromEntityIDs  []string
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
}

type RecallRelationshipHit struct {
	TeamID           string
	RelationshipID   string
	SemanticGroupKey string
	SubjectEntityID  string
	SubjectName      string
	PredicateKey     string
	ObjectEntityID   string
	ObjectValueID    string
	ObjectName       string
	ObjectValueType  string
	ObjectValue      string
	Polarity         string
	ScopeKey         string
	ValidFrom        *time.Time
	Score            float64
	Rank             int
	SearchState      string
	SupportCount     int
	SourceGroupCount int
	CreatedAt        time.Time
}
