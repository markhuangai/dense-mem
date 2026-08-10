package http

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// registerPublicRoutes registers public routes that do not require authentication,
// profile resolution, or rate limiting. These routes are used for health checks
// and readiness probes in container orchestration environments.
func registerPublicRoutes(e *echo.Echo, healthConfig HealthConfig) {
	// Health endpoint - simple liveness check with optional degraded status
	// Returns 200 {"status":"ok"} or 200 {"status":"ok","degraded":true,"reason":"..."}
	// when running in in-memory mode.
	e.GET("/health", handleHealth(healthConfig))

	// Ready endpoint - readiness check with dependency validation.
	// Optional check failures are reported as degraded without blocking readiness.
	e.GET("/ready", handleReady(healthConfig.Checks))
}

// handleHealth handles the /health endpoint.
// It always returns 200. In normal mode it returns {"status":"ok","checks":{...}}.
// In degraded (in-memory) mode it returns 200 with degraded=true, a reason, and checks.
func handleHealth(healthConfig HealthConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		checks := make(map[string]string)
		for _, result := range runDependencyChecks(ctx, healthConfig.Checks) {
			if result.Err != nil || result.TimedOut {
				if result.Check.Optional {
					checks[result.Check.Name] = "degraded"
				} else {
					checks[result.Check.Name] = "failed"
				}
			} else {
				checks[result.Check.Name] = "ok"
			}
		}

		if healthConfig.Degraded {
			return c.JSON(http.StatusOK, map[string]any{
				"status":   "ok",
				"degraded": true,
				"reason":   healthConfig.Reason,
				"checks":   checks,
			})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status": "ok",
			"checks": checks,
		})
	}
}

// handleReady returns a handler function for the /ready endpoint.
// It executes all health checks and returns the appropriate status.
func handleReady(checks []HealthCheck) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		dependencies := make(map[string]string)
		allPass := true

		for _, result := range runDependencyChecks(ctx, checks) {
			if result.Err != nil || result.TimedOut {
				if result.Check.Optional {
					dependencies[result.Check.Name] = "degraded"
				} else {
					dependencies[result.Check.Name] = "failed"
					allPass = false
				}
			} else {
				dependencies[result.Check.Name] = "ok"
			}
		}

		if allPass {
			return c.JSON(http.StatusOK, map[string]interface{}{
				"status":       "ready",
				"dependencies": dependencies,
			})
		}

		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
			"status":       "degraded",
			"dependencies": dependencies,
		})
	}
}
