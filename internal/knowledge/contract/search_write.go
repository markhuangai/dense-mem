package contract

import (
	"context"
	"time"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

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

type SearchReconciliationSelectionInput struct {
	RunID               string
	EmbeddingContractID string
	EmbeddingDimensions int
	Limit               int
}

type SearchReconciliationRunInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	Now                 time.Time
	StaleAfter          time.Duration
}

type FinishSearchReconciliationRunInput struct {
	RunID         string
	Status        string
	SelectedCount int64
	EmbeddedCount int64
	UpdatedCount  int64
	DriftedCount  int64
	LastError     string
}

type ApplySearchReconciliationInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	Documents           []SearchDocumentEmbedding
}

type SearchReconciliationApplyResult struct {
	UpdatedCount          int64
	SkippedCount          int64
	RemainingDriftedCount int64
}

type SearchReconciliationRun struct {
	RunID         string
	LocalRunDate  time.Time
	Status        string
	SelectedCount int64
	EmbeddedCount int64
	UpdatedCount  int64
	DriftedCount  int64
	LastError     string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	UpdatedAt     time.Time
}

type SearchDocumentDriftCount struct {
	Class string
	Count int64
}

type SearchConvergenceInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	// Contract is selected by the Search capability and passed across the
	// database-free projection boundary. Knowledge must not reselect it.
	Contract *ActiveSearchContract
}

type SearchConvergence struct {
	ObservedAt        time.Time
	Status            string
	Contract          *ActiveSearchContract
	ExpectedDocuments int64
	CurrentDocuments  int64
	DriftedDocuments  int64
	AffectedTeamCount int64
	OldestDriftAge    time.Duration
	DriftClasses      []SearchDocumentDriftCount
	LatestRun         *SearchReconciliationRun
}

// SearchProjectionRepository is the Knowledge-owned persistence port for
// canonical search projection repair. It carries no database handles.
type SearchProjectionRepository interface {
	ReserveSearchReconciliationRun(context.Context, SearchReconciliationRunInput) (*SearchReconciliationRun, bool, error)
	SelectSearchReconciliationDocuments(context.Context, SearchReconciliationSelectionInput) ([]SearchDocumentForEmbedding, error)
	CompleteSearchReconciliationDocuments(context.Context, ApplySearchReconciliationInput) (*SearchReconciliationApplyResult, error)
	FinishSearchReconciliationRun(context.Context, FinishSearchReconciliationRunInput) error
	GetSearchConvergence(context.Context, SearchConvergenceInput) (*SearchConvergence, error)
}

// SearchDocumentOwner is the Knowledge-owned persistence surface shared by
// semantic writers and Search reconciliation. It contains only typed
// operations and keeps database handles inside the PostgreSQL adapter.
type SearchDocumentOwner interface {
	SearchProjectionRepository
	UpsertSearchDocument(context.Context, UpsertSearchDocumentInput) (*SearchDocumentResult, error)
	LoadSearchDocumentsForEmbedding(context.Context, LoadSearchDocumentsForEmbeddingInput) ([]SearchDocumentForEmbedding, error)
	LoadSearchDocumentsForSources(context.Context, LoadSearchDocumentsForSourcesInput) ([]SearchDocumentForEmbedding, error)
	CompleteSearchDocumentsWithEmbeddings(context.Context, CompleteSearchDocumentsWithEmbeddingsInput) error
}
