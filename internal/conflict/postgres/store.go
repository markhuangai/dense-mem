package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// Persistence combines the Conflict workflow port with the cited-evidence
// mutation port. Canonical terminal effects remain implemented by Knowledge;
// this adapter supplies Conflict-owned reads and transaction boundaries.
type Persistence interface {
	ResolveEvidenceConflict(context.Context, conflictcontract.EvidenceConflictResolutionInput) (*conflictcontract.EvidenceConflictCaseRecord, error)
}

type Store struct {
	db          *gorm.DB
	rls         storagepostgres.RLSHelper
	persistence Persistence
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper, persistence Persistence) *Store {
	return &Store{db: db, rls: rls, persistence: persistence}
}

func (r *Store) withSystemTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *Store) withSystemReadOnlyRepeatableTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict: rls helper is required")
	}
	return r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, fn)
}

var _ conflictcontract.EvidenceConflictRepository = (*Store)(nil)
