package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

type (
	RecallEvidenceInput            = recallcontract.RecallEvidenceInput
	RecallRelationshipsInput       = recallcontract.RecallRelationshipsInput
	RecallEvidenceResult           = recallcontract.RecallEvidenceResult
	RecallRelationshipsResult      = recallcontract.RecallRelationshipsResult
	RecallEvidenceHit              = recallcontract.RecallEvidenceHit
	RecallRelationshipHit          = recallcontract.RecallRelationshipHit
	ActiveSearchContract           = searchcontract.ActiveSearchContract
	SearchHit                      = searchcontract.SearchHit
	RelationshipConflictCaseRecord = tracecontract.RelationshipConflictCaseRecord
	EvidenceConflictCaseRecord     = recallcontract.EvidenceConflictCaseRecord
	EvidenceConflictEventRecord    = recallcontract.EvidenceConflictEventRecord
	EvidenceConflictPositionRecord = recallcontract.EvidenceConflictPositionRecord
)

const (
	EvidenceConflictMaxResults = knowledgecontract.EvidenceConflictMaxResults
)

var (
	ErrSearchContractMismatch   = knowledgecontract.ErrSearchContractMismatch
	ErrEvidenceConflictNotFound = knowledgecontract.ErrEvidenceConflictNotFound
)

// Store is Recall's PostgreSQL read adapter. It owns query construction,
// visibility predicates, temporal fences, and hydration while using the
// existing Search adapter for the active embedding contract.
type Store struct {
	db                    *gorm.DB
	rls                   storagepostgres.RLSHelper
	search                searchcontract.SearchRepository
	relationshipConflicts RelationshipConflictReader
	evidenceConflicts     EvidenceConflictReader
}

// Source is the narrow construction seam used by composition. It exposes
// database mechanics and conflict readers without exposing them to the Recall
// application service.
type Source interface {
	RecallDatabase() *gorm.DB
	RecallRLS() storagepostgres.RLSHelper
	RecallSearchRepository() searchcontract.SearchRepository
	RecallRelationshipConflictReader() RelationshipConflictReader
	RecallEvidenceConflictReader() EvidenceConflictReader
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper, search searchcontract.SearchRepository, relationshipConflicts RelationshipConflictReader, evidenceConflicts EvidenceConflictReader) *Store {
	return &Store{
		db:                    db,
		rls:                   rls,
		search:                search,
		relationshipConflicts: relationshipConflicts,
		evidenceConflicts:     evidenceConflicts,
	}
}

func NewStoreFromSource(source Source) *Store {
	if source == nil {
		return nil
	}
	return NewStore(source.RecallDatabase(), source.RecallRLS(), source.RecallSearchRepository(), source.RecallRelationshipConflictReader(), source.RecallEvidenceConflictReader())
}

func (r *Store) GetActiveSearchContract(ctx context.Context) (*ActiveSearchContract, error) {
	if r == nil || r.search == nil {
		return nil, errors.New("recall: search adapter is required")
	}
	return r.search.GetActiveSearchContract(ctx)
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("recall: database is required")
	}
	if r.rls == nil {
		return errors.New("recall: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *Store) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("recall: database is required")
	}
	return r.db, nil
}

var _ recallcontract.Repository = (*Store)(nil)
