package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

// New constructs the Evidence provenance reader with the shared RLS helper.
func New(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("trace: database is required")
	}
	if r.rls == nil {
		return errors.New("trace: rls helper is required")
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID.String() == teamID && actor.OwnerID != uuid.Nil {
		return r.rls.WithTeamProfileReadOnlyRepeatableTx(ctx, r.db, teamID, actor.OwnerID.String(), fn)
	}
	return r.rls.WithTeamReadOnlyRepeatableTx(ctx, r.db, teamID, fn)
}
