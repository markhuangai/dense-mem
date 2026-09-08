package contract

import searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"

type ActiveSearchContract = searchcontract.ActiveSearchContract
type FullTextSearchInput = searchcontract.FullTextSearchInput
type ExactVectorSearchInput = searchcontract.ExactVectorSearchInput

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

type LoadSearchDocumentsForEmbeddingInput struct {
	TeamID            string
	OwnerProfileID    string
	SearchDocumentIDs []string
}

type LoadSearchDocumentsForSourcesInput struct {
	TeamID         string
	OwnerProfileID string
	SourceKind     string
	SourceIDs      []string
}

// SearchDocumentResult is the durable projection identity returned by a
// semantic write. It contains no provider response or database handle.
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
	SpaceID                string
	SpaceGeneration        int64
}

type SearchDocumentForEmbedding struct {
	SearchDocumentResult
	DocumentText       string
	DocumentHash       string
	StoredDocumentHash string
	Retired            bool
}

type InlineEmbeddingPlan struct {
	Documents               []SearchDocumentForEmbedding
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type InlineEmbeddingResult struct {
	DocumentHash            string
	Embedding               []float32
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

type SearchDocumentEmbedding struct {
	TeamID                 string
	SearchDocumentID       string
	OwnerProfileID         string
	SourceKind             string
	SourceID               string
	DocumentText           string
	DocumentHash           string
	StoredDocumentHash     string
	SourceVersion          int64
	ProjectionFormat       int
	ProjectionGenerationID string
	DocumentVersion        int64
	EmbeddingContractID    string
	EmbeddingDimensions    int
	Embedding              []float32
	SpaceID                string
	SpaceGeneration        int64
	Retired                bool
}

type CompleteSearchDocumentsWithEmbeddingsInput struct {
	TeamID         string
	OwnerProfileID string
	Documents      []SearchDocumentEmbedding
}
