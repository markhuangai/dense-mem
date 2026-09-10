// Package postgres owns the search read and maintenance SQL adapters.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var (
	ErrSearchStaleVersion                 = knowledgecontract.ErrSearchStaleVersion
	ErrSearchContractMismatch             = knowledgecontract.ErrSearchContractMismatch
	ErrSearchEmbeddingRequired            = knowledgecontract.ErrSearchEmbeddingRequired
	ErrSearchConvergenceAttentionRequired = errors.New("search convergence is attention_required")
)

// Store is the private PostgreSQL implementation of the search read and
// maintenance ports. Canonical search-document writes remain owned by the
// knowledge PostgreSQL store.
type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

var _ searchmaintenance.SearchMaintenanceRepository = (*Store)(nil)

func (r *Store) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("search: database is required")
	}
	return r.db, nil
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *Store) withSystemTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *Store) withActiveSystemTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
			return fmt.Errorf("failed to set app.current_team_id: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func ensureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	return storagepostgres.EnsureActiveTeamForMutation(ctx, tx, teamID)
}

func (r *Store) sqlDB() (*sql.DB, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("search: sql database: %w", err)
	}
	return sqlDB, nil
}
