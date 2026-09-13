package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const defaultCleanupBatchSize = 100

// Repository owns the bounded PostgreSQL query used by the disposable demo
// cleanup worker. Deletion policy remains in the demo application service.
type Repository struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *Repository {
	return &Repository{db: db, rls: rls}
}

func (r *Repository) ExpiredTeamIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return nil, fmt.Errorf("demo cleanup repository unavailable")
	}
	if limit <= 0 {
		limit = defaultCleanupBatchSize
	}

	var ids []uuid.UUID
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id
			FROM teams
			WHERE deleted_at IS NULL
			  AND metadata @> '{"demo": true}'::jsonb
			  AND metadata->>'demo_expires_at' IS NOT NULL
			  AND (metadata->>'demo_expires_at')::timestamptz <= ?
			ORDER BY created_at ASC
			LIMIT ?
		`, now.UTC(), limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list expired demo teams: %w", err)
	}
	return ids, nil
}
