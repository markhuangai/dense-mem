package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

func (r *Store) withActiveTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("search: database is required")
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *Store) withSystemReadOnlyRepeatableTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("knowledge: database is required")
	}
	if r.rls == nil {
		return errors.New("knowledge: rls helper is required")
	}
	return r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, fn)
}
