package middleware

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// ProfileAuthorizationService is the interface for services needed by profile authorization middleware.
// This interface allows for mocking in tests.
type ProfileAuthorizationService interface {
	CrossProfileDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error
}

// profileAuthorizationService wraps an AuditService to implement ProfileAuthorizationService.
type profileAuthorizationService struct {
	auditSvc service.AuditService
}

// Ensure profileAuthorizationService implements ProfileAuthorizationService.
var _ ProfileAuthorizationService = (*profileAuthorizationService)(nil)

// NewProfileAuthorizationService creates a new ProfileAuthorizationService from an AuditService.
func NewProfileAuthorizationService(auditSvc service.AuditService) ProfileAuthorizationService {
	return &profileAuthorizationService{auditSvc: auditSvc}
}

func (s *profileAuthorizationService) CrossProfileDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.auditSvc.CrossProfileDenied(ctx, actorProfileID, targetProfileID, operation, metadata, clientIP, correlationID)
}

// AuthorizeProfile creates a middleware that enforces team-based authorization.
//
// Authorization rules:
// 1. If no target profile is in context (no ResolvedProfileKey), pass through silently.
// 2. If principal has a ProfileID that matches the target profile, allow access.
// 3. Otherwise, deny with 403 FORBIDDEN and audit CrossProfileDenied.
//
// This middleware must run after both AuthMiddleware and ProfileResolutionMiddleware.
func AuthorizeProfile(authzSvc ProfileAuthorizationService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			principal := GetPrincipal(ctx)

			// Fail closed: if no principal is set, authentication middleware must have
			// run before this one. Missing principal means the middleware chain is
			// misconfigured — deny the request rather than pass through.
			if principal == nil {
				return httperr.New(httperr.FORBIDDEN, "authentication required")
			}

			// Get target profile from context (set by ProfileResolutionMiddleware)
			targetProfileID, hasTargetProfile := GetResolvedProfileID(ctx)

			// If no target profile in context, pass through silently
			if !hasTargetProfile {
				return next(c)
			}

			// Team-bound principals must match the target team.
			principalTeamID := principal.GetTeamID()
			if principalTeamID == targetProfileID {
				return next(c)
			}

			// Authorization denied - audit and return 403
			actorProfileID := ""
			if principalTeamID != uuid.Nil {
				actorProfileID = principalTeamID.String()
			}

			// Log cross-profile access denial
			if authzSvc != nil {
				_ = authzSvc.CrossProfileDenied(
					ctx,
					actorProfileID,
					targetProfileID.String(),
					"profile_access",
					nil,
					c.RealIP(),
					GetCorrelationID(ctx),
				)
			}

			return httperr.New(httperr.FORBIDDEN, "access denied to this profile")
		}
	}
}
