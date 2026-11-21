package ontology

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

// PreparationCooldown belongs to a disposable local seed runtime.
type PreparationCooldown struct {
	mu    sync.Mutex
	path  string
	now   func() time.Time
	until time.Time
	err   error
}

func RestorePreparationCooldown(file string) (*PreparationCooldown, error) {
	if file == "" {
		return nil, nil
	}
	if !filepath.IsAbs(file) {
		return nil, errors.New("ontology preparation checkpoint must be absolute")
	}
	gate := &PreparationCooldown{path: file, now: time.Now}
	contents, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		// Distinguish an empty checkpoint from a missing mount.
		if dir, err := os.Stat(filepath.Dir(file)); err == nil && dir.IsDir() {
			return gate, nil
		}
	}
	if err == nil {
		gate.until, err = time.Parse(time.RFC3339Nano, string(contents))
	}
	if err != nil {
		return nil, errors.New("ontology preparation cooldown checkpoint is invalid or unavailable")
	}
	return gate, nil
}

func (c *PreparationCooldown) check() error {
	return c.admit(nil)
}

func (c *PreparationCooldown) admit(dispatch func() error) error {
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil || c.now().Before(c.until) {
			// Native finalization returns unadmitted work to pending on this outcome.
			return contract.ErrMaintenancePaused
		}
	}
	if dispatch != nil {
		return dispatch()
	}
	return nil
}

func (c *PreparationCooldown) hold(ctx context.Context, logger *slog.Logger, retryAt time.Time) {
	c.mu.Lock()
	now := c.now()
	until := now.Add(time.Minute)
	if retryAt.After(until) {
		until = retryAt
	}
	if until.After(c.until) {
		c.until = until
		if err := c.save(); err != nil {
			c.err = err
		}
	}
	until, failed := c.until, c.err != nil
	c.mu.Unlock()
	if logger == nil {
		return
	}
	scope := observability.ExchangeScopeFromContext(ctx)
	logger.WarnContext(ctx, "ontology preparation rate limited", "http_status", 429, "hold_until", until,
		"team_id", scope.TeamID, "run_id", scope.RunID, "batch_id", scope.BatchID, "attempt", scope.Attempt)
	if failed {
		logger.ErrorContext(ctx, "ontology preparation cooldown checkpoint unavailable", "run_id", scope.RunID, "batch_id", scope.BatchID)
	}
}

func (c *PreparationCooldown) save() error {
	file, err := os.CreateTemp(filepath.Dir(c.path), ".cooldown-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(c.until.UTC().Format(time.RFC3339Nano))
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), c.path)
}
