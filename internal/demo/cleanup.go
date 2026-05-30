package demo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const defaultCleanupBatchSize = 100

type ExpiredTeamRepository interface {
	ExpiredTeamIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error)
}

type Repository struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

func NewRepository(db *gorm.DB, rls postgres.RLSHelper) *Repository {
	return &Repository{db: db, rls: rls}
}

func (r *Repository) ExpiredTeamIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return nil, fmt.Errorf("demo cleanup repository unavailable")
	}
	if limit <= 0 {
		limit = defaultCleanupBatchSize
	}

	ids := make([]uuid.UUID, 0)
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

type Cleaner struct {
	repo       ExpiredTeamRepository
	profiles   service.ProfileService
	dataPurger service.ProfileDataPurger
	interval   time.Duration
	batch      int
	now        func() time.Time
}

func NewCleaner(repo ExpiredTeamRepository, profiles service.ProfileService, interval time.Duration) *Cleaner {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Cleaner{
		repo:     repo,
		profiles: profiles,
		interval: interval,
		batch:    defaultCleanupBatchSize,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func NewCleanerWithDataPurger(repo ExpiredTeamRepository, profiles service.ProfileService, dataPurger service.ProfileDataPurger, interval time.Duration) *Cleaner {
	cleaner := NewCleaner(repo, profiles, interval)
	cleaner.dataPurger = dataPurger
	return cleaner
}

func (c *Cleaner) PurgeExpired(ctx context.Context) error {
	if c == nil || c.repo == nil || c.profiles == nil {
		return fmt.Errorf("demo cleaner unavailable")
	}
	ids, err := c.repo.ExpiredTeamIDs(ctx, c.now().UTC(), c.batch)
	if err != nil {
		return err
	}

	var joined error
	for _, id := range ids {
		if c.dataPurger != nil {
			if err := c.dataPurger.PurgeProfileData(ctx, id.String()); err != nil {
				joined = errors.Join(joined, fmt.Errorf("pre-purge expired demo team data %s: %w", id.String(), err))
				continue
			}
		}
		if err := c.profiles.Delete(ctx, id, nil, "demo_cleanup", "", "demo-cleanup"); err != nil {
			if apiErr, ok := err.(*httperr.APIError); ok && apiErr.Code == httperr.NOT_FOUND {
				continue
			}
			joined = errors.Join(joined, fmt.Errorf("delete expired demo team %s: %w", id.String(), err))
		}
	}
	return joined
}

func (c *Cleaner) Start(ctx context.Context) func(context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var once sync.Once

	go func() {
		defer close(done)
		_ = c.PurgeExpired(runCtx)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = c.PurgeExpired(runCtx)
			}
		}
	}()

	return func(ctx context.Context) error {
		once.Do(cancel)
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
