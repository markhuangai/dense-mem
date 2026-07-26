package middleware

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// ProfileResolutionServiceInterface defines the minimal interface the middleware
// needs to resolve a profile. Handler and middleware share the GetByID method
// name on the concrete ProfileService implementation, so the middleware
// re-declares only that single method here to avoid importing the handler
// package and creating a cycle.
type ProfileResolutionServiceInterface interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
}

// ResolvedProfileKey is the typed context key for storing the resolved team ID.
// Using a typed context key prevents accidental overwrites from other packages.
type ResolvedProfileKey struct{}

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

func isHeaderScopedProfileRoute(path string) bool {
	headerScopedPrefixes := []string{
		"/mcp",
		"/ui/api/recall",
		"/ui/api/dreaming",
		"/ui/api/dreams",
		"/ui/api/evidence",
		"/ui/api/relationships",
		"/ui/api/communities",
	}

	for _, prefix := range headerScopedPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}

	return false
}

// ProfileResolutionMiddleware creates a middleware that resolves and validates
// team IDs from path parameters or the authenticated key.
//
// For profile-scoped routes (/ui/api/team/profiles/:profileId/*): reads :profileId param
// For principal-scoped routes (/mcp, /ui/api/recall, /ui/api/dreaming,
// /ui/api/dreams): uses the authenticated principal's team binding.
//
// The middleware:
// - Validates that a profile ID is provided (returns 400 PROFILE_ID_REQUIRED if missing)
// - Validates the UUID format (returns 400 INVALID_UUID if malformed)
// - Resolves the profile through the service (returns 404 NOT_FOUND if not found or deleted)
// - Stores the resolved profile ID in context for downstream use
//
// This middleware does NOT perform authorization - it only resolves and loads.
func ProfileResolutionMiddleware(svc ProfileResolutionServiceInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			principal := GetPrincipal(ctx)

			var profileIDStr string
			var isToolRoute bool

			// Determine route type and extract profile ID accordingly
			path := c.Request().URL.Path

			if isHeaderScopedProfileRoute(path) {
				// Canonical team-scoped routes use the team-bound API key.
				isToolRoute = true
				if principal != nil && principal.GetTeamID() != uuid.Nil {
					profileIDStr = principal.GetTeamID().String()
				}
			} else if strings.HasPrefix(path, "/ui/api/team/profiles/") {
				// Team-profile route: read from :teamId path param, with :profileId support.
				isToolRoute = false
				profileIDStr = c.Param("teamId")
				if profileIDStr == "" {
					profileIDStr = c.Param("profileId")
				}
			} else {
				// Not a route that needs profile resolution, pass through
				return next(c)
			}

			// Validate profile ID is present
			if profileIDStr == "" {
				return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
			}

			// Parse and validate UUID
			profileID, err := uuid.Parse(profileIDStr)
			if err != nil {
				return httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
			}

			// Resolve profile through service
			// The service returns NOT_FOUND for non-existent or soft-deleted profiles
			profile, err := svc.GetByID(ctx, profileID)
			if err != nil {
				// Check if it's a NOT_FOUND error
				if apiErr, ok := err.(*httperr.APIError); ok && apiErr.Code == httperr.NOT_FOUND {
					return httperr.New(httperr.NOT_FOUND, "profile not found")
				}
				// Other errors are internal
				return httperr.New(httperr.INTERNAL_ERROR, "failed to resolve profile")
			}

			if profile == nil {
				return httperr.New(httperr.NOT_FOUND, "profile not found")
			}

			// Store resolved team ID and metadata in context.
			// Use dedicated typed keys to prevent collisions.
			resolvedCtx := context.WithValue(ctx, ResolvedProfileKey{}, profileID)
			resolvedCtx = context.WithValue(resolvedCtx, ResolvedTeamContextKey{}, ResolvedTeamContext{
				ID:          profileID,
				Name:        profile.Name,
				Description: profile.Description,
			})
			c.SetRequest(c.Request().WithContext(resolvedCtx))

			// Continue to next handler
			_ = principal   // Principal is available for authorization decisions in downstream middleware
			_ = isToolRoute // Route type available for future authorization logic

			return next(c)
		}
	}
}

// GetResolvedProfileID retrieves the resolved team ID from the context.
// Returns the profile ID and true if found, or uuid.Nil and false if not found.
func GetResolvedProfileID(ctx context.Context) (uuid.UUID, bool) {
	if id, ok := ctx.Value(ResolvedProfileKey{}).(uuid.UUID); ok {
		return id, true
	}
	return uuid.Nil, false
}

// GetResolvedTeamID retrieves the resolved team ID from the context.
func GetResolvedTeamID(ctx context.Context) (uuid.UUID, bool) {
	return GetResolvedProfileID(ctx)
}

// GetResolvedTeamContext retrieves the resolved team metadata from the context.
func GetResolvedTeamContext(ctx context.Context) (ResolvedTeamContext, bool) {
	team, ok := ctx.Value(ResolvedTeamContextKey{}).(ResolvedTeamContext)
	return team, ok
}

// MustGetResolvedProfileID retrieves the resolved profile ID from the context.
// Panics if not found. Use only when profile resolution is guaranteed.
func MustGetResolvedProfileID(ctx context.Context) uuid.UUID {
	id, ok := GetResolvedProfileID(ctx)
	if !ok {
		panic("profile resolution middleware: profile ID not found in context")
	}
	return id
}

// MustGetResolvedTeamID retrieves the resolved team ID from the context.
func MustGetResolvedTeamID(ctx context.Context) uuid.UUID {
	return MustGetResolvedProfileID(ctx)
}

// SetResolvedProfileIDForTest sets a resolved profile ID in context for testing purposes.
// This is intended for use in unit tests to bypass profile resolution middleware.
func SetResolvedProfileIDForTest(ctx context.Context, profileID uuid.UUID) context.Context {
	return context.WithValue(ctx, ResolvedProfileKey{}, profileID)
}

// SetResolvedTeamIDForTest sets a resolved team ID in context for testing.
func SetResolvedTeamIDForTest(ctx context.Context, teamID uuid.UUID) context.Context {
	return SetResolvedProfileIDForTest(ctx, teamID)
}

// SetResolvedTeamContextForTest sets resolved team metadata in context for tests.
func SetResolvedTeamContextForTest(ctx context.Context, team ResolvedTeamContext) context.Context {
	if team.ID != uuid.Nil {
		ctx = SetResolvedProfileIDForTest(ctx, team.ID)
	}
	return context.WithValue(ctx, ResolvedTeamContextKey{}, team)
}
