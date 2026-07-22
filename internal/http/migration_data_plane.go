package http

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

const migrationDataPlaneGateStatusTTL = 5 * time.Second

type MigrationDataPlaneStatusProvider interface {
	Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
}

func migrationDataPlaneGate(statusProvider MigrationDataPlaneStatusProvider) echo.MiddlewareFunc {
	return migrationDataPlaneGateWithTTL(statusProvider, migrationDataPlaneGateStatusTTL)
}

func migrationDataPlaneGateWithTTL(statusProvider MigrationDataPlaneStatusProvider, ttl time.Duration) echo.MiddlewareFunc {
	if statusProvider == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	cache := &migrationDataPlaneStatusCache{
		statusProvider: statusProvider,
		ttl:            ttl,
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			status, err := cache.Status(c.Request().Context())
			if err != nil || status == nil {
				if err != nil {
					c.Logger().Error("migration data plane status unavailable")
				}
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "migration state unavailable")
			}
			if status.DataPlaneAllowed {
				return next(c)
			}
			message := strings.TrimSpace(status.ReadinessMessage)
			if message == "" {
				message = "legacy migration is required; data plane is disabled"
			}
			return httperr.New(httperr.SERVICE_UNAVAILABLE, message)
		}
	}
}

type migrationDataPlaneStatusCache struct {
	statusProvider MigrationDataPlaneStatusProvider
	ttl            time.Duration
	mu             sync.Mutex
	status         *domain.V2MigrationControlStatus
	err            error
	expiresAt      time.Time
}

func (c *migrationDataPlaneStatusCache) Status(ctx context.Context) (*domain.V2MigrationControlStatus, error) {
	if c.ttl <= 0 {
		return c.statusProvider.Status(ctx)
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.expiresAt.IsZero() && now.Before(c.expiresAt) {
		return cloneMigrationDataPlaneStatus(c.status), c.err
	}
	status, err := c.statusProvider.Status(ctx)
	c.status = cloneMigrationDataPlaneStatus(status)
	c.err = err
	c.expiresAt = now.Add(c.ttl)
	return cloneMigrationDataPlaneStatus(c.status), err
}

func cloneMigrationDataPlaneStatus(status *domain.V2MigrationControlStatus) *domain.V2MigrationControlStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}
