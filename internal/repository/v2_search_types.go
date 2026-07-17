package repository

import (
	"context"
	"time"
)

type V2SearchRepository interface {
	GetActiveSearchProfile(ctx context.Context, profileKey string) (*V2SearchProfile, error)
	CheckSearchReadiness(ctx context.Context, profileKey string) (*V2SearchReadiness, error)
	UpsertSearchDocument(ctx context.Context, input V2UpsertSearchDocumentInput) (*V2SearchDocumentResult, error)
	ClaimEmbeddingJobs(ctx context.Context, input V2ClaimEmbeddingJobsInput) ([]V2EmbeddingJob, error)
	CompleteEmbeddingJob(ctx context.Context, input V2CompleteEmbeddingJobInput) error
	SearchFullText(ctx context.Context, input V2FullTextSearchInput) ([]V2SearchHit, error)
	SearchExactVector(ctx context.Context, input V2ExactVectorSearchInput) ([]V2SearchHit, error)
}

type V2SearchProfile struct {
	ProfileKey            string
	EmbeddingContractID   string
	SearchIndexProfileID  string
	RankingProfileID      string
	EmbeddingDimensions   int
	EmbeddingProvider     string
	EmbeddingModel        string
	DistanceMetric        string
	VectorNormalization   string
	DocumentFormatVersion int
	QueryFormatVersion    int
	IndexStrategy         string
	OperatorClass         string
	IndexedExpression     string
	PhysicalIndexName     string
	ExactMaxRows          int
	CandidateLimit        int
	AllowExactFallback    bool
}

type V2SearchReadiness struct {
	ProfileKey string
	Ready      bool
	Reasons    []V2SearchReadinessReason
	Profile    *V2SearchProfile
}

type V2SearchReadinessReason struct {
	Code    string
	Message string
}

type V2UpsertSearchDocumentInput struct {
	TeamID              string
	OwnerProfileID      string
	ProfileKey          string
	SourceKind          string
	SourceID            string
	SourceVersion       int64
	DocumentText        string
	DocumentHash        string
	EmbeddingContractID string
	Metadata            map[string]any
}

type V2SearchDocumentResult struct {
	TeamID              string
	SearchDocumentID    string
	OwnerProfileID      string
	SourceKind          string
	SourceID            string
	SourceVersion       int64
	DocumentVersion     int64
	EmbeddingContractID string
	EmbeddingDimensions int
	SearchState         string
	QueuedJobID         string
}

type V2ClaimEmbeddingJobsInput struct {
	TeamID   string
	WorkerID string
	Limit    int
	Lease    time.Duration
}

type V2EmbeddingJob struct {
	TeamID              string
	EmbeddingJobID      string
	SearchDocumentID    string
	OwnerProfileID      string
	SourceKind          string
	SourceID            string
	SourceVersion       int64
	DocumentVersion     int64
	EmbeddingContractID string
	EmbeddingDimensions int
	Status              string
	Attempts            int
	LeaseUntil          *time.Time
	DocumentText        string
}

type V2CompleteEmbeddingJobInput struct {
	TeamID         string
	EmbeddingJobID string
	WorkerID       string
	Embedding      []float32
}

type V2FullTextSearchInput struct {
	TeamID     string
	Query      string
	SourceKind string
	Limit      int
}

type V2ExactVectorSearchInput struct {
	TeamID              string
	ProfileKey          string
	EmbeddingContractID string
	SourceKind          string
	QueryEmbedding      []float32
	Limit               int
}

type V2SearchHit struct {
	TeamID              string
	SearchDocumentID    string
	SourceKind          string
	SourceID            string
	SourceVersion       int64
	DocumentVersion     int64
	EmbeddingContractID string
	Distance            float64
	TextRank            float64
}
