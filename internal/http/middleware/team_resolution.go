package middleware

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// TeamResolutionServiceInterface defines the minimal team lookup used by the
// middleware without importing the handler package.
type TeamResolutionServiceInterface interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
}

// ResolvedTeamIDKey is the typed context key for storing the resolved team ID.
// Using a typed context key prevents accidental overwrites from other packages.
type ResolvedTeamIDKey struct{}

// ResolvedTeamContextKey is the typed context key for storing the resolved team
// metadata needed by downstream MCP surfaces.
type ResolvedTeamContextKey struct{}

// ResolvedTeamContext contains non-secret team metadata resolved from the
// authenticated API key or route. It is safe to expose to authenticated callers.
type ResolvedTeamContext struct {
	ID          uuid.UUID
	Name        string
	Description string
}

func isTeamResolvedRoute(path string) bool {
	headerScopedPrefixes := []string{
		"/mcp",
		"/ui/api/recall",
		"/ui/api/dreaming",
		"/ui/api/dreams",
	}

	for _, prefix := range headerScopedPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}

	return false
}

// TeamResolutionMiddleware resolves the authenticated team for MCP and
// first-party memory routes. Authorization remains a separate middleware.
func TeamResolutionMiddleware(svc TeamResolutionServiceInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			principal := GetPrincipal(ctx)
			path := c.Request().URL.Path
			if !isTeamResolvedRoute(path) {
				return next(c)
			}

			if principal == nil || principal.GetTeamID() == uuid.Nil {
				return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
			}
			teamID := principal.GetTeamID()

			if svc == nil {
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "team service unavailable")
			}

			team, err := svc.GetByID(ctx, teamID)
			if err != nil {
				if apiErr, ok := err.(*httperr.APIError); ok && apiErr.Code == httperr.NOT_FOUND {
					return httperr.New(httperr.NOT_FOUND, "team not found")
				}
				return httperr.New(httperr.INTERNAL_ERROR, "failed to resolve team")
			}
			if team == nil {
				return httperr.New(httperr.NOT_FOUND, "team not found")
			}

			resolvedCtx := context.WithValue(ctx, ResolvedTeamIDKey{}, teamID)
			resolvedCtx = context.WithValue(resolvedCtx, ResolvedTeamContextKey{}, ResolvedTeamContext{
				ID:          teamID,
				Name:        team.Name,
				Description: team.Description,
			})
			c.SetRequest(c.Request().WithContext(resolvedCtx))
			return next(c)
		}
	}
}

// GetResolvedTeamID retrieves the resolved team ID from the context.
func GetResolvedTeamID(ctx context.Context) (uuid.UUID, bool) {
	if id, ok := ctx.Value(ResolvedTeamIDKey{}).(uuid.UUID); ok {
		return id, true
	}
	return uuid.Nil, false
}

// GetResolvedTeamContext retrieves the resolved team metadata from the context.
func GetResolvedTeamContext(ctx context.Context) (ResolvedTeamContext, bool) {
	team, ok := ctx.Value(ResolvedTeamContextKey{}).(ResolvedTeamContext)
	return team, ok
}

// MustGetResolvedTeamID retrieves the resolved team ID from the context.
func MustGetResolvedTeamID(ctx context.Context) uuid.UUID {
	id, ok := GetResolvedTeamID(ctx)
	if !ok {
		panic("team resolution middleware: team ID not found in context")
	}
	return id
}

// SetResolvedTeamIDForTest sets a resolved team ID in context for testing.
func SetResolvedTeamIDForTest(ctx context.Context, teamID uuid.UUID) context.Context {
	return context.WithValue(ctx, ResolvedTeamIDKey{}, teamID)
}

// SetResolvedTeamContextForTest sets resolved team metadata in context for tests.
func SetResolvedTeamContextForTest(ctx context.Context, team ResolvedTeamContext) context.Context {
	if team.ID != uuid.Nil {
		ctx = SetResolvedTeamIDForTest(ctx, team.ID)
	}
	return context.WithValue(ctx, ResolvedTeamContextKey{}, team)
}
