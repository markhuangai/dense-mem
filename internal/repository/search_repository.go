package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type inlineEmbeddingResultsContextKey struct{}

// WithInlineEmbeddingResults carries provider vectors into a fenced semantic
// transaction. The provider must have completed before this context is used.
func WithInlineEmbeddingResults(ctx context.Context, results []InlineEmbeddingResult) context.Context {
	copyResults := make([]InlineEmbeddingResult, len(results))
	for index, result := range results {
		copyResults[index] = InlineEmbeddingResult{
			DocumentHash:            result.DocumentHash,
			Embedding:               append([]float32(nil), result.Embedding...),
			EmbeddingContractID:     result.EmbeddingContractID,
			EmbeddingDimensions:     result.EmbeddingDimensions,
			EmbeddingModel:          result.EmbeddingModel,
			SearchIndexGenerationID: result.SearchIndexGenerationID,
			IndexGeneration:         result.IndexGeneration,
		}
	}
	return context.WithValue(ctx, inlineEmbeddingResultsContextKey{}, copyResults)
}

func inlineEmbeddingResults(ctx context.Context) []InlineEmbeddingResult {
	value, _ := ctx.Value(inlineEmbeddingResultsContextKey{}).([]InlineEmbeddingResult)
	return value
}

var (
	ErrSearchStaleVersion                 = knowledgecontract.ErrSearchStaleVersion
	ErrSearchContractMismatch             = knowledgecontract.ErrSearchContractMismatch
	ErrSearchEmbeddingRequired            = knowledgecontract.ErrSearchEmbeddingRequired
	ErrSearchConvergenceAttentionRequired = searchpostgres.ErrSearchConvergenceAttentionRequired
	ErrInlineEmbeddingPlanMismatch        = knowledgecontract.ErrInlineEmbeddingPlanMismatch
	ErrInlineEmbeddingPlanTooLarge        = knowledgecontract.ErrInlineEmbeddingPlanTooLarge
)

// SearchRepositoryImpl remains a compatibility facade. Query, bootstrap,
// convergence, and reconciliation policy live in search/postgres; canonical
// search-document writes continue to use the knowledge owner.
type SearchRepositoryImpl struct {
	db                *gorm.DB
	rls               rLSHelper
	knowledgeOwner    *knowledgepostgres.Store
	searchOwner       *searchpostgres.Store
	recallConflicts   recallpostgres.RelationshipConflictReader
	evidenceConflicts recallpostgres.EvidenceConflictReader
}

var _ SearchRepository = (*SearchRepositoryImpl)(nil)

func NewSearchRepository(db *gorm.DB, rls *postgres.RLS) *SearchRepositoryImpl {
	return &SearchRepositoryImpl{
		db: db, rls: rls,
		knowledgeOwner:    knowledgepostgres.NewStore(db, rls, knowledgecontract.ConflictRuntimeConfig{}),
		searchOwner:       searchpostgres.NewStore(db, rls),
		recallConflicts:   recallpostgres.RelationshipConflictReader(loadRecallOpenConflictRecords),
		evidenceConflicts: recallpostgres.EvidenceConflictReader(loadRecallEvidenceConflictRecords),
	}
}

func (r *SearchRepositoryImpl) searchReadOwner() (*searchpostgres.Store, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("search: database is required")
	}
	if r.searchOwner == nil {
		r.searchOwner = searchpostgres.NewStore(r.db, r.rls)
	}
	return r.searchOwner, nil
}

func (r *SearchRepositoryImpl) GetActiveSearchContract(ctx context.Context) (*ActiveSearchContract, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.GetActiveSearchContract(ctx)
}

func (r *SearchRepositoryImpl) CheckSearchReadiness(ctx context.Context) (*SearchReadiness, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.CheckSearchReadiness(ctx)
}

func (r *SearchRepositoryImpl) SearchFullText(ctx context.Context, input FullTextSearchInput) ([]SearchHit, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.SearchFullText(ctx, input)
}

func (r *SearchRepositoryImpl) SearchExactVector(ctx context.Context, input ExactVectorSearchInput) ([]SearchHit, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.SearchExactVector(ctx, input)
}

func (r *SearchRepositoryImpl) UpsertSearchDocument(ctx context.Context, input UpsertSearchDocumentInput) (*SearchDocumentResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("search: knowledge write owner is required")
	}
	return owner.UpsertSearchDocument(ctx, knowledgepostgres.UpsertSearchDocumentInput(input))
}

func (r *SearchRepositoryImpl) EnsureActiveSearchContract(ctx context.Context, input EnsureActiveSearchContractInput) (*EnsureActiveSearchContractResult, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
	if err != nil || result == nil {
		return nil, err
	}
	return &EnsureActiveSearchContractResult{
		Contract:             result.Contract,
		CreatedContract:      result.CreatedContract,
		CreatedGeneration:    result.CreatedGeneration,
		CreatedPhysicalIndex: result.CreatedPhysicalIndex,
	}, nil
}

func (r *SearchRepositoryImpl) GetSearchConvergence(ctx context.Context, input SearchConvergenceInput) (*SearchConvergence, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	value, err := owner.GetSearchConvergence(ctx, searchmaintenance.SearchConvergenceInput{
		EmbeddingContractID: input.EmbeddingContractID,
		EmbeddingDimensions: input.EmbeddingDimensions,
	})
	if err != nil || value == nil {
		return nil, err
	}
	return searchConvergenceFromNative(value), nil
}

func (r *SearchRepositoryImpl) CheckSearchConvergence(ctx context.Context) error {
	owner, err := r.searchReadOwner()
	if err != nil {
		return err
	}
	return owner.CheckSearchConvergence(ctx)
}

func (r *SearchRepositoryImpl) ReserveSearchReconciliationRun(ctx context.Context, input SearchReconciliationRunInput) (*SearchReconciliationRun, bool, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, false, err
	}
	run, claimed, err := owner.ReserveSearchReconciliationRun(ctx, searchmaintenance.SearchReconciliationRunInput{
		EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions,
		Now: input.Now, StaleAfter: input.StaleAfter,
	})
	return searchReconciliationRunFromNative(run), claimed, err
}

func (r *SearchRepositoryImpl) SelectSearchReconciliationDocuments(ctx context.Context, input SearchReconciliationSelectionInput) ([]SearchDocumentForEmbedding, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	documents, err := owner.SelectSearchReconciliationDocuments(ctx, searchmaintenance.SearchReconciliationSelectionInput{
		RunID: input.RunID, EmbeddingContractID: input.EmbeddingContractID,
		EmbeddingDimensions: input.EmbeddingDimensions, Limit: input.Limit,
	})
	return searchDocumentsFromNative(documents), err
}

func (r *SearchRepositoryImpl) CompleteSearchReconciliationDocuments(ctx context.Context, input ApplySearchReconciliationInput) (*SearchReconciliationApplyResult, error) {
	owner, err := r.searchReadOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.CompleteSearchReconciliationDocuments(ctx, searchApplyInput(input))
	return searchApplyResultFromNative(result), err
}

func (r *SearchRepositoryImpl) FinishSearchReconciliationRun(ctx context.Context, input FinishSearchReconciliationRunInput) error {
	owner, err := r.searchReadOwner()
	if err != nil {
		return err
	}
	return owner.FinishSearchReconciliationRun(ctx, searchmaintenance.FinishSearchReconciliationRunInput{
		RunID: input.RunID, Status: input.Status, SelectedCount: input.SelectedCount,
		EmbeddedCount: input.EmbeddedCount, UpdatedCount: input.UpdatedCount,
		DriftedCount: input.DriftedCount, LastError: input.LastError,
	})
}

func (r *SearchRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("search: database is required")
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *SearchRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("search: database is required")
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *SearchRepositoryImpl) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("search: database is required")
	}
	return r.db, nil
}
