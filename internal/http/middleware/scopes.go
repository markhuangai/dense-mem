package middleware

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
)

// RequireScopes creates a middleware that enforces scope requirements.
//
// Authorization rules:
// 1. If principal has all required scopes, allow access.
// 2. Otherwise, deny with 403 FORBIDDEN.
//
// This middleware must run after AuthMiddleware.
func RequireScopes(required ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			principal := GetPrincipal(ctx)

			// If no principal, deny (RequireAuth should have been used first)
			if principal == nil {
				return httperr.New(httperr.FORBIDDEN, "authentication required")
			}

			// Check that principal has all required scopes
			if hasAllScopes(principal.Scopes, required) {
				return next(c)
			}

			// Missing required scope(s)
			return httperr.New(httperr.FORBIDDEN, "insufficient permissions")
		}
	}
}

// RequireRole creates a middleware that enforces an administrative role.
//
// This middleware must run after AuthMiddleware.
func RequireRole(required string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := GetPrincipal(c.Request().Context())
			if principal == nil {
				return httperr.New(httperr.FORBIDDEN, "authentication required")
			}
			if principal.Role == required {
				return next(c)
			}
			return httperr.New(httperr.FORBIDDEN, "insufficient permissions")
		}
	}
}

// hasAllScopes checks if the provided scopes contain all required scopes.
func hasAllScopes(scopes []string, required []string) bool {
	scopeSet := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		scopeSet[s] = true
	}

	for _, r := range required {
		if !scopeSet[r] {
			return false
		}
	}

	return true
}
