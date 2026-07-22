package http

import (
	"context"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type MigrationDataPlaneStatusProvider interface {
	Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
}

func migrationDataPlaneGate(statusProvider MigrationDataPlaneStatusProvider) echo.MiddlewareFunc {
	if statusProvider == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			status, err := statusProvider.Status(c.Request().Context())
			if err != nil || status == nil {
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
