package maintenance

import (
	"context"
	"time"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type ActiveSearchContract = searchcontract.ActiveSearchContract

// EnsureActiveSearchContractInput is the startup configuration used to make
// the active embedding and search-index generation available.
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

// SearchDocumentForEmbedding is the version-fenced search projection
// snapshot passed to the reconciliation provider.
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

type SearchConvergenceInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
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

type SearchDocumentDriftCount struct {
	Class string
	Count int64
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

// SearchReconciliationRepository is the narrow maintenance port. It
// contains only search reads and document-fenced reconciliation operations;
// canonical semantic writes remain owned by knowledge/postgres.
type SearchReconciliationRepository interface {
	searchcontract.SearchRepository
	ReserveSearchReconciliationRun(context.Context, SearchReconciliationRunInput) (*SearchReconciliationRun, bool, error)
	SelectSearchReconciliationDocuments(context.Context, SearchReconciliationSelectionInput) ([]SearchDocumentForEmbedding, error)
	CompleteSearchReconciliationDocuments(context.Context, ApplySearchReconciliationInput) (*SearchReconciliationApplyResult, error)
	FinishSearchReconciliationRun(context.Context, FinishSearchReconciliationRunInput) error
}

type SearchMaintenanceRepository interface {
	searchcontract.SearchRepository
	SearchReconciliationRepository
	EnsureActiveSearchContract(context.Context, EnsureActiveSearchContractInput) (*EnsureActiveSearchContractResult, error)
	GetSearchConvergence(context.Context, SearchConvergenceInput) (*SearchConvergence, error)
	CheckSearchConvergence(context.Context) error
}
