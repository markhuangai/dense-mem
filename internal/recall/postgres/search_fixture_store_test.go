package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type searchFixtureStore struct {
	db         *gorm.DB
	rls        storagepostgres.RLSHelper
	read       *searchpostgres.Store
	projection *knowledgepostgres.Store
	recall     *Store
}

func newSearchFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *searchFixtureStore {
	read := searchpostgres.NewStore(db, rls)
	relationshipReader := func(context.Context, *gorm.DB, string, *time.Time, []RecallEvidenceHit) ([]RelationshipConflictCaseRecord, error) {
		return []RelationshipConflictCaseRecord{}, nil
	}
	return &searchFixtureStore{
		db:         db,
		rls:        rls,
		read:       read,
		projection: knowledgepostgres.NewStore(db, rls, knowledgecontract.ConflictRuntimeConfig{}),
		recall:     NewStore(db, rls, read, relationshipReader, LoadRecallEvidenceConflictRecords),
	}
}

func (s *searchFixtureStore) GetActiveSearchContract(ctx context.Context) (*searchcontract.ActiveSearchContract, error) {
	return s.read.GetActiveSearchContract(ctx)
}
func (s *searchFixtureStore) CheckSearchReadiness(ctx context.Context) (*searchcontract.SearchReadiness, error) {
	return s.read.CheckSearchReadiness(ctx)
}
func (s *searchFixtureStore) SearchFullText(ctx context.Context, input searchcontract.FullTextSearchInput) ([]searchcontract.SearchHit, error) {
	return s.read.SearchFullText(ctx, input)
}
func (s *searchFixtureStore) SearchExactVector(ctx context.Context, input searchcontract.ExactVectorSearchInput) ([]searchcontract.SearchHit, error) {
	return s.read.SearchExactVector(ctx, input)
}
func (s *searchFixtureStore) RecallEvidence(ctx context.Context, input RecallEvidenceInput) (*RecallEvidenceResult, error) {
	return s.recall.RecallEvidence(ctx, input)
}
func (s *searchFixtureStore) RecallRelationships(ctx context.Context, input RecallRelationshipsInput) (*RecallRelationshipsResult, error) {
	return s.recall.RecallRelationships(ctx, input)
}
func (s *searchFixtureStore) UpsertSearchDocument(ctx context.Context, input knowledgecontract.UpsertSearchDocumentInput) (*knowledgecontract.SearchDocumentResult, error) {
	return s.projection.UpsertSearchDocument(ctx, input)
}
func (s *searchFixtureStore) LoadSearchDocumentsForEmbedding(ctx context.Context, input knowledgecontract.LoadSearchDocumentsForEmbeddingInput) ([]knowledgecontract.SearchDocumentForEmbedding, error) {
	return s.projection.LoadSearchDocumentsForEmbedding(ctx, input)
}
func (s *searchFixtureStore) LoadSearchDocumentsForSources(ctx context.Context, input knowledgecontract.LoadSearchDocumentsForSourcesInput) ([]knowledgecontract.SearchDocumentForEmbedding, error) {
	return s.projection.LoadSearchDocumentsForSources(ctx, input)
}
func (s *searchFixtureStore) CompleteSearchDocumentsWithEmbeddings(ctx context.Context, input knowledgecontract.CompleteSearchDocumentsWithEmbeddingsInput) error {
	return s.projection.CompleteSearchDocumentsWithEmbeddings(ctx, input)
}
func (s *searchFixtureStore) ReserveSearchReconciliationRun(ctx context.Context, input searchmaintenance.SearchReconciliationRunInput) (*searchmaintenance.SearchReconciliationRun, bool, error) {
	return s.projection.ReserveSearchReconciliationRun(ctx, input)
}
func (s *searchFixtureStore) SelectSearchReconciliationDocuments(ctx context.Context, input searchmaintenance.SearchReconciliationSelectionInput) ([]searchmaintenance.SearchDocumentForEmbedding, error) {
	return s.projection.SelectSearchReconciliationDocuments(ctx, input)
}
func (s *searchFixtureStore) CompleteSearchReconciliationDocuments(ctx context.Context, input searchmaintenance.ApplySearchReconciliationInput) (*searchmaintenance.SearchReconciliationApplyResult, error) {
	return s.projection.CompleteSearchReconciliationDocuments(ctx, input)
}
func (s *searchFixtureStore) FinishSearchReconciliationRun(ctx context.Context, input searchmaintenance.FinishSearchReconciliationRunInput) error {
	return s.projection.FinishSearchReconciliationRun(ctx, input)
}
func (s *searchFixtureStore) GetSearchConvergence(ctx context.Context, input searchmaintenance.SearchConvergenceInput) (*searchmaintenance.SearchConvergence, error) {
	return s.projection.GetSearchConvergence(ctx, input)
}

var _ searchcontract.SearchRepository = (*searchFixtureStore)(nil)
var _ knowledgecontract.SearchDocumentOwner = (*searchFixtureStore)(nil)
var _ knowledgecontract.SearchProjectionRepository = (*searchFixtureStore)(nil)
