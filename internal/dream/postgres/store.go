// Package postgres contains Dream's PostgreSQL adapter. It owns Dream SQL,
// transaction boundaries, and the evaluation read branch while exposing only
// the capability contracts to application code.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// Source is the narrow construction seam used by the legacy repository while
// the application composition migrates to the Dream-owned adapter.
type Source interface {
	DreamDatabase() *gorm.DB
	DreamRLS() postgres.RLSHelper
}

// Store is the complete Dream PostgreSQL adapter. Its methods are implemented
// in this package so the legacy repository can remain a forwarding facade.
type Store struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

func NewStore(db *gorm.DB, rls postgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

func NewStoreFromSource(source Source) *Store {
	if source == nil {
		return nil
	}
	return NewStore(source.DreamDatabase(), source.DreamRLS())
}

var _ dreamcontract.DreamRepository = (*Store)(nil)
var _ dreamcontract.ScheduledDreamRepository = (*Store)(nil)
var _ dreamcontract.DreamControlRepository = (*Store)(nil)
var _ dreamcontract.EvidenceDiscoveryRepository = (*Store)(nil)
var _ dreamcontract.EvidenceDiscoveryInputValidator = (*Store)(nil)

func (r *Store) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("dream: database is required")
	}
	if r.rls == nil {
		return errors.New("dream: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := postgres.EnsureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
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

func marshalJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func marshalJSONArray(value []map[string]any) ([]byte, error) {
	if value == nil {
		value = []map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json array: %w", err)
	}
	return data, nil
}
