// Package contract contains the search capability's consumer-owned read port.
package contract

import "context"

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

type SearchReadinessReason struct {
	Code    string
	Message string
}

type SearchReadiness struct {
	Ready    bool
	Reasons  []SearchReadinessReason
	Contract *ActiveSearchContract
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

type SearchRepository interface {
	GetActiveSearchContract(context.Context) (*ActiveSearchContract, error)
	CheckSearchReadiness(context.Context) (*SearchReadiness, error)
	SearchFullText(context.Context, FullTextSearchInput) ([]SearchHit, error)
	SearchExactVector(context.Context, ExactVectorSearchInput) ([]SearchHit, error)
}
