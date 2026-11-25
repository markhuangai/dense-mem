package postgres

import (
	"context"
	"errors"

	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type Store struct {
	db             *gorm.DB
	rls            storage.RLSHelper
	commitEvidence func(context.Context, *gorm.DB, knowledge.SynchronousRememberCommitInput, []knowledge.InlineEmbeddingResult) (*knowledge.SynchronousRememberCommitResult, error)
	lock           func(context.Context, string, string, string, func() error) error
}

func New(db *gorm.DB, rls storage.RLSHelper, commit func(context.Context, *gorm.DB, knowledge.SynchronousRememberCommitInput, []knowledge.InlineEmbeddingResult) (*knowledge.SynchronousRememberCommitResult, error), lock func(context.Context, string, string, string, func() error) error) *Store {
	return &Store{db: db, rls: rls, commitEvidence: commit, lock: lock}
}

func (r *Store) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil || r.rls == nil {
		return errors.New("session: database and RLS helper are required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := storage.EnsureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}
