package repository

import (
	"context"
	"errors"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

func (r *SearchRepositoryImpl) RecallDatabase() *gorm.DB {
	if r == nil {
		return nil
	}
	return r.db
}

func (r *SearchRepositoryImpl) RecallRLS() postgres.RLSHelper {
	if r == nil {
		return nil
	}
	return r.rls
}

func (r *SearchRepositoryImpl) RecallSearchRepository() searchcontract.SearchRepository {
	if r == nil {
		return nil
	}
	return r
}

func (r *SearchRepositoryImpl) RecallRelationshipConflictReader() recallpostgres.RelationshipConflictReader {
	return recallpostgres.RelationshipConflictReader(loadRecallOpenConflictRecords)
}

func (r *SearchRepositoryImpl) RecallEvidenceConflictReader() recallpostgres.EvidenceConflictReader {
	return recallpostgres.EvidenceConflictReader(recallpostgres.LoadRecallEvidenceConflictRecords)
}

func (r *SearchRepositoryImpl) recallReadOwner() (*recallpostgres.Store, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("recall: database is required")
	}
	owner := recallpostgres.NewStoreFromSource(r)
	if owner == nil {
		return nil, errors.New("recall: native adapter is required")
	}
	return owner, nil
}

func normalizeRecallUUIDList(values []string) []string {
	return recallpostgres.NormalizeRecallUUIDList(values)
}

// RecallEvidence remains a single-hop compatibility entry point. Recall SQL
// and hydration are owned by the native PostgreSQL adapter.
func (r *SearchRepositoryImpl) RecallEvidence(ctx context.Context, input RecallEvidenceInput) (*RecallEvidenceResult, error) {
	owner, err := r.recallReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.RecallEvidence(ctx, input)
}

var _ recallcontract.Repository = (*SearchRepositoryImpl)(nil)
