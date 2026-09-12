package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/httperr"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

const defaultCleanupBatchSize = 100

type ExpiredTeamRepository interface {
	ExpiredTeamIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error)
}

type Cleaner struct {
	repo     ExpiredTeamRepository
	teams    accessservice.TeamService
	interval time.Duration
	batch    int
	now      func() time.Time
}

func NewCleaner(repo ExpiredTeamRepository, teams accessservice.TeamService, interval time.Duration) *Cleaner {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Cleaner{
		repo:     repo,
		teams:    teams,
		interval: interval,
		batch:    defaultCleanupBatchSize,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (c *Cleaner) PurgeExpired(ctx context.Context) error {
	if c == nil || c.repo == nil || c.teams == nil {
		return fmt.Errorf("demo cleaner unavailable")
	}
	ids, err := c.repo.ExpiredTeamIDs(ctx, c.now().UTC(), c.batch)
	if err != nil {
		return err
	}

	var joined error
	for _, id := range ids {
		if err := c.teams.Delete(ctx, id, nil, "demo_cleanup", "", "demo-cleanup"); err != nil {
			if apiErr, ok := err.(*httperr.APIError); ok && apiErr.Code == httperr.NOT_FOUND {
				continue
			}
			joined = errors.Join(joined, fmt.Errorf("delete expired demo team %s: %w", id.String(), err))
		}
	}
	return joined
}

// Run executes the cleanup loop until the process context is canceled.
func (c *Cleaner) Run(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("demo cleaner unavailable")
	}
	_ = c.PurgeExpired(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = c.PurgeExpired(ctx)
		}
	}
}

func (c *Cleaner) Name() string {
	return "demo cleanup"
}
