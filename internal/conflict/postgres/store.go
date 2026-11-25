package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
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

var _ conflictcontract.EvidenceConflictRepository = (*Store)(nil)
