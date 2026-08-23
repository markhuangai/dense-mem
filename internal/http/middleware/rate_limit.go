package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/httperr"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

// RateLimitMiddleware creates a rate limiting middleware using the fixed-window algorithm.
func RateLimitMiddleware(svc accessservice.RateLimitServiceInterface, cfg config.ConfigProvider, auditSvc accessservice.AuditService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get the principal from context
			principal := GetPrincipal(c.Request().Context())
			if principal == nil {
				// No principal means auth middleware didn't run - let it through
				// so auth errors take precedence
				return next(c)
			}

			if principal.OwnerID == uuid.Nil {
				return httperr.New(httperr.FORBIDDEN, "authentication required")
			}
			ownerID := principal.OwnerID.String()

			// Get route path for stable bucket
			routePath := rateLimitRoutePath(c.Path())

			limit := selectRateLimit(cfg)
			if principal.RateLimit > 0 && principal.RateLimit < limit {
				limit = principal.RateLimit
			}

			// Perform rate limit check
			ctx := c.Request().Context()
			subjectID := principal.OwnerID
			if principal.CredentialID != nil {
				subjectID = *principal.CredentialID
			}
			rateLimitSubject := ownerID + ":key:" + subjectID.String()
			allowed, remaining, resetAt, err := svc.Check(ctx, rateLimitSubject, routePath, limit)
			if err != nil {
				c.Logger().Errorf("rate limit check failed: %v", err)
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "rate limit service unavailable")
			}

			// Set rate limit headers on all responses
			c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
			c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			c.Response().Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if !allowed {
				// Calculate retry-after seconds
				retryAfter := int(time.Until(resetAt).Seconds())
				if retryAfter < 0 {
					retryAfter = 0
				}
				c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))

				// Log and audit rate limit hit
				logRateLimit(c, auditSvc, ownerID, routePath, limit, remaining, resetAt)

				return httperr.New(httperr.RATE_LIMITED, "rate limit exceeded")
			}

			return next(c)
		}
	}
}

func rateLimitRoutePath(routePath string) string {
	switch routePath {
	case "/mcp", "/teams/:teamId/mcp":
		return "/mcp"
	default:
		return routePath
	}
}

// logRateLimit logs and audits a rate limit event.
func logRateLimit(c echo.Context, auditSvc accessservice.AuditService, profileID, routePath string, limit, remaining int, resetAt time.Time) {
	if auditSvc == nil {
		return
	}

	clientIP := c.RealIP()
	correlationID := GetCorrelationID(c.Request().Context())

	metadata := map[string]interface{}{
		"route_path": routePath,
		"limit":      limit,
		"remaining":  remaining,
		"reset_at":   resetAt.Unix(),
	}

	// Use a background context with timeout for logging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var profileIDPtr *string
	if profileID != "" {
		profileIDPtr = &profileID
	}

	_ = auditSvc.RateLimited(ctx, profileIDPtr, "request", metadata, clientIP, correlationID)
}

func selectRateLimit(cfg config.ConfigProvider) int {
	return cfg.GetRateLimitPerMinute()
}
