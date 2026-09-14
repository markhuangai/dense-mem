package maintenance

import (
	"context"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
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
type SearchDocumentResult = knowledgecontract.SearchDocumentResult
type SearchDocumentForEmbedding = knowledgecontract.SearchDocumentForEmbedding
type UpsertSearchDocumentInput = knowledgecontract.UpsertSearchDocumentInput
type SearchDocumentEmbedding = knowledgecontract.SearchDocumentEmbedding

type SearchConvergenceInput = knowledgecontract.SearchConvergenceInput
type SearchConvergence = knowledgecontract.SearchConvergence
type SearchDocumentDriftCount = knowledgecontract.SearchDocumentDriftCount
type SearchReconciliationRun = knowledgecontract.SearchReconciliationRun
type SearchReconciliationSelectionInput = knowledgecontract.SearchReconciliationSelectionInput
type SearchReconciliationRunInput = knowledgecontract.SearchReconciliationRunInput
type FinishSearchReconciliationRunInput = knowledgecontract.FinishSearchReconciliationRunInput
type ApplySearchReconciliationInput = knowledgecontract.ApplySearchReconciliationInput
type SearchReconciliationApplyResult = knowledgecontract.SearchReconciliationApplyResult

// SearchReadRepository is the query and index policy surface owned by Search.
type SearchReadRepository interface {
	searchcontract.SearchRepository
}

type SearchMaintenanceRepository interface {
	SearchReadRepository
	EnsureActiveSearchContract(context.Context, EnsureActiveSearchContractInput) (*EnsureActiveSearchContractResult, error)
}
