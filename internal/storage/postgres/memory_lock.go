package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// LockMemorySpaceTx prevents a concurrent private-space seal or generation
// advance from invalidating the active-generation fence after it is checked.
// The database helper performs the row lock in system mode because PostgreSQL
// applies the memory-space update policy to row-locked reads under runtime RLS.
func LockMemorySpaceTx(ctx context.Context, tx *gorm.DB, teamID, spaceID string) error {
	if strings.TrimSpace(spaceID) == "" {
		return sql.ErrNoRows
	}
	var locked bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT dense_mem_lock_memory_space(?::uuid, ?::uuid)
	`, teamID, spaceID).Row().Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return sql.ErrNoRows
	}
	return nil
}

func IsLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}
