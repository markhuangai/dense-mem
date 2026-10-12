// Package postgres retains scoped Dream history, provenance, and diagnostic retention.
package postgres

import (
	"context"

	"errors"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// Store is the complete Dream PostgreSQL adapter. Its methods are implemented
// in this package so the application service can depend on Dream-owned ports.
type Store struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

func NewStore(db *gorm.DB, rls postgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("dream: database is required")
	}
	if r.rls == nil {
		return errors.New("dream: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}
