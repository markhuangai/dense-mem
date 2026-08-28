package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type teamProfileTransactionContextKey struct{}

type teamProfileTransaction struct {
	teamID    string
	profileID string
	tx        *gorm.DB
}

func (r *LedgerRepositoryImpl) withAtomicRememberTx(ctx context.Context, teamID, profileID string, fn func(context.Context) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		scoped := context.WithValue(ctx, teamProfileTransactionContextKey{}, teamProfileTransaction{
			teamID: teamID, profileID: profileID, tx: tx,
		})
		return fn(scoped)
	})
}
