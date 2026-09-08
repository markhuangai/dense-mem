package repository

import (
	"context"
	"time"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type SearchRepository = searchcontract.SearchRepository

// SearchReconciliationRepository owns the document-centric drift repair
// boundary. It deliberately exposes no queue, lease, worker, or job concepts.
type SearchReconciliationRepository interface {
	SearchRepository
	ReserveSearchReconciliationRun(ctx context.Context, input SearchReconciliationRunInput) (*SearchReconciliationRun, bool, error)
	SelectSearchReconciliationDocuments(ctx context.Context, input SearchReconciliationSelectionInput) ([]SearchDocumentForEmbedding, error)
	CompleteSearchReconciliationDocuments(ctx context.Context, input ApplySearchReconciliationInput) (*SearchReconciliationApplyResult, error)
	FinishSearchReconciliationRun(ctx context.Context, input FinishSearchReconciliationRunInput) error
}

// InlineEmbeddingRepository is the optional write-path capability used by
// synchronous semantic writers. Keeping it separate from SearchRepository
// avoids forcing read-only test doubles to implement a request-scoped write
// concern.
type InlineEmbeddingRepository interface {
	LoadSearchDocumentsForEmbedding(ctx context.Context, input LoadSearchDocumentsForEmbeddingInput) ([]SearchDocumentForEmbedding, error)
	LoadSearchDocumentsForSources(ctx context.Context, input LoadSearchDocumentsForSourcesInput) ([]SearchDocumentForEmbedding, error)
	CompleteSearchDocumentsWithEmbeddings(ctx context.Context, input CompleteSearchDocumentsWithEmbeddingsInput) error
}

type RecallRepository = recallcontract.Repository

type ActiveSearchContract = searchcontract.ActiveSearchContract
type SearchReadiness = searchcontract.SearchReadiness
type SearchReadinessReason = searchcontract.SearchReadinessReason

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

type SearchDocumentResult = knowledgecontract.SearchDocumentResult

// LoadSearchDocumentsForEmbeddingInput identifies one owner-scoped batch of
// documents whose text and version fences are needed before an inline write
// embedding call.
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

type SearchDocumentForEmbedding = knowledgecontract.SearchDocumentForEmbedding

// InlineEmbeddingPlan is the provider-independent render result for one
// synchronous semantic write. Search document IDs are provisional plan keys;
// the commit phase maps returned vectors to the final version-fenced rows by
// document hash.
type InlineEmbeddingPlan = knowledgecontract.InlineEmbeddingPlan

// InlineEmbeddingResult carries one validated provider vector back to the
// fenced semantic commit. DocumentHash is the stable render identity; the
// final search_document_id is assigned or loaded by PostgreSQL.
type InlineEmbeddingResult = knowledgecontract.InlineEmbeddingResult

// CompleteSearchDocumentsWithEmbeddingsInput carries the provider output back
// across the fenced storage boundary. Every document is checked against the
// source/document/contract/space versions loaded before the provider call.
type CompleteSearchDocumentsWithEmbeddingsInput struct {
	TeamID         string
	OwnerProfileID string
	Documents      []SearchDocumentEmbedding
}

type ApplySearchReconciliationInput struct {
	EmbeddingContractID string
	EmbeddingDimensions int
	Documents           []SearchDocumentEmbedding
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

// SearchConvergence is an operator-facing document drift projection. A
// document is current only when its active-contract vector is present and its
// source/document version and hash still match the canonical projection.
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

// SearchReconciliationRun is the durable run summary retained for the
// document-centric reconciliation pass. It deliberately has no worker,
// lease, canary, or queue identifiers.
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

// SearchReconciliationSelectionInput bounds one document-centric repair
// snapshot to the active embedding contract.
type SearchReconciliationSelectionInput struct {
	// RunID identifies the durable reconciliation pass whose keyset cursor is
	// advanced before canonical hydration. Empty keeps the selection bounded
	// without persisting a cursor for callers that only need a one-off snapshot.
	RunID               string
	EmbeddingContractID string
	EmbeddingDimensions int
	Limit               int
}

// SearchReconciliationRunInput starts one bounded repair pass. The
// reservation is short-lived; provider work happens after its transaction
// closes.
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

type SearchReconciliationApplyResult struct {
	UpdatedCount int64
	SkippedCount int64
	// RemainingDriftedCount is a bounded count of selected items that could not
	// be applied. The operator convergence projection owns the exact global
	// count.
	RemainingDriftedCount int64
}

type FullTextSearchInput = searchcontract.FullTextSearchInput
type ExactVectorSearchInput = searchcontract.ExactVectorSearchInput
type SearchHit = searchcontract.SearchHit

type RecallEvidenceInput = recallcontract.RecallEvidenceInput
type RecallRelationshipsInput = recallcontract.RecallRelationshipsInput
type RecallEvidenceResult = recallcontract.RecallEvidenceResult
type RecallRelationshipsResult = recallcontract.RecallRelationshipsResult
type RecallEvidenceHit = recallcontract.RecallEvidenceHit
type RecallRelationshipHit = recallcontract.RecallRelationshipHit
