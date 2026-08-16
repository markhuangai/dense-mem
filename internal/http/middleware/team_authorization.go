package middleware

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// TeamAuthorizationService is the interface used by team authorization middleware.
// This interface allows for mocking in tests.
type TeamAuthorizationService interface {
	CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error
}

type teamAuthorizationService struct {
	auditSvc service.AuditService
}

var _ TeamAuthorizationService = (*teamAuthorizationService)(nil)

// NewTeamAuthorizationService creates a new TeamAuthorizationService from an AuditService.
func NewTeamAuthorizationService(auditSvc service.AuditService) TeamAuthorizationService {
	return &teamAuthorizationService{auditSvc: auditSvc}
}

func (s *teamAuthorizationService) CrossTeamDenied(ctx context.Context, actorTeamID, targetTeamID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return s.auditSvc.CrossTeamDenied(ctx, actorTeamID, targetTeamID, operation, metadata, clientIP, correlationID)
}

// AuthorizeTeam creates a middleware that enforces team-based authorization.
//
// Authorization rules:
// 1. If no target team is in context, pass through silently.
// 2. If the principal team matches the target team, allow access.
// 3. Otherwise, deny with 403 FORBIDDEN and audit CrossTeamDenied.
//
// This middleware must run after both AuthMiddleware and TeamResolutionMiddleware.
func AuthorizeTeam(authzSvc TeamAuthorizationService) echo.MiddlewareFunc {
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

			targetTeamID, hasTargetTeam := GetResolvedTeamID(ctx)

			if !hasTargetTeam {
				return next(c)
			}

			// Team-bound principals must match the target team.
			principalTeamID := principal.GetTeamID()
			if principalTeamID == targetTeamID {
				return next(c)
			}

			// Authorization denied - audit and return 403
			actorTeamID := ""
			if principalTeamID != uuid.Nil {
				actorTeamID = principalTeamID.String()
			}

			// Log cross-team access denial
			if authzSvc != nil {
				_ = authzSvc.CrossTeamDenied(
					ctx,
					actorTeamID,
					targetTeamID.String(),
					"team_access",
					nil,
					c.RealIP(),
					GetCorrelationID(ctx),
				)
			}

			return httperr.New(httperr.FORBIDDEN, "access denied to this team")
		}
	}
}
