package postgres

import (
	"context"
	"errors"
	"github.com/markhuangai/dense-mem/internal/observability"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontologycontract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type (
	RecallEvidenceInput            = recallcontract.RecallEvidenceInput
	RecallEvidenceResult           = recallcontract.RecallEvidenceResult
	RecallEvidenceHit              = recallcontract.RecallEvidenceHit
	ActiveSearchContract           = searchcontract.ActiveSearchContract
	SearchHit                      = searchcontract.SearchHit
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
	db                  *gorm.DB
	rls                 storagepostgres.RLSHelper
	search              searchcontract.SearchRepository
	evidenceConflicts   EvidenceConflictReader
	topics              func(context.Context, *gorm.DB, string, ontologycontract.TopicReadInput) (ontologycontract.TopicReadResult, error)
	ontology            func() OntologyReader
	snapshot            *gorm.DB
	snapshotTeam        string
	organizationFailure string
	topicLogger         observability.LogProvider
}

func (r *Store) WithTopicDiagnostics(logger observability.LogProvider) *Store {
	r.topicLogger = logger
	return r
}

type OntologyReader func(context.Context, *gorm.DB, string, ontologycontract.RecallReadInput) (ontologycontract.RecallOrganization, error)

func (r *Store) WithOntology(reader func() OntologyReader) *Store {
	r.ontology = reader
	return r
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper, search searchcontract.SearchRepository, evidenceConflicts EvidenceConflictReader) *Store {
	return &Store{
		db:                db,
		rls:               rls,
		search:            search,
		evidenceConflicts: evidenceConflicts,
	}
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
	if r.snapshot != nil {
		if teamID != r.snapshotTeam {
			return ontologycontract.ErrUnauthorized
		}
		return fn(r.snapshot.WithContext(ctx))
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

func (r *Store) WithTopics(reader func(context.Context, *gorm.DB, string, ontologycontract.TopicReadInput) (ontologycontract.TopicReadResult, error)) *Store {
	r.topics = reader
	return r
}
