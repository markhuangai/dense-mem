package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

// New constructs the trace PostgreSQL adapter with the shared RLS helper and
// existing graph/conflict readers.
func New(db *gorm.DB, rls storagepostgres.RLSHelper, graph GraphLoader, conflicts ConflictLoader) *Store {
	return &Store{db: db, rls: rls, graph: graph, conflicts: conflicts}
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("trace: database is required")
	}
	if r.rls == nil {
		return errors.New("trace: rls helper is required")
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID.String() == teamID && actor.OwnerID != uuid.Nil {
		return r.rls.WithTeamProfileTx(ctx, r.db, teamID, actor.OwnerID.String(), fn)
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}
