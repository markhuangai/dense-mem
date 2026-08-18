package http

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// OAuthProtectedResourceProvider supplies the runtime RFC 9728 contract.
type OAuthProtectedResourceProvider interface {
	OAuthProtectedResourceMetadata(context.Context) (service.OAuthProtectedResourceMetadata, error)
	MCPPublicBaseURL(context.Context) (string, error)
}

type oauthProtectedResourceDocument struct {
	Resource             string   `json:"resource"`
	ResourceName         string   `json:"resource_name"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

// RegisterOAuthProtectedResourceRoutes publishes the unscoped and team-scoped
// RFC 9728 metadata documents. Disabled OAuth providers remain undiscoverable.
func RegisterOAuthProtectedResourceRoutes(e *echo.Echo, provider OAuthProtectedResourceProvider) {
	if e == nil || provider == nil {
		return
	}
	handler := oauthProtectedResourceMetadataHandler(provider)
	e.GET("/.well-known/oauth-protected-resource/mcp", handler)
	e.GET("/.well-known/oauth-protected-resource/teams/:teamId/mcp", handler)
}

func oauthProtectedResourceMetadataHandler(provider OAuthProtectedResourceProvider) echo.HandlerFunc {
	return func(c echo.Context) error {
		teamID, err := oauthMetadataTeamID(c)
		if err != nil {
			return err
		}
		metadata, err := provider.OAuthProtectedResourceMetadata(c.Request().Context())
		if err != nil {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "oauth resource metadata unavailable")
		}
		if len(metadata.AuthorizationServers) == 0 {
			return httperr.New(httperr.NOT_FOUND, "oauth protected resource is not configured")
		}
		resourceURL := oauthResourceURL(c, provider, teamID)
		if resourceURL == "" {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "oauth resource metadata unavailable")
		}
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		return c.JSON(nethttp.StatusOK, oauthProtectedResourceDocument{
			Resource:             resourceURL,
			ResourceName:         "Dense-Mem MCP",
			AuthorizationServers: metadata.AuthorizationServers,
			ScopesSupported:      metadata.ScopesSupported,
			BearerMethods:        []string{"header"},
		})
	}
}

func oauthProtectedResourceChallenge(provider OAuthProtectedResourceProvider) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err == nil || provider == nil || oauthErrorStatus(err) != nethttp.StatusUnauthorized {
				return err
			}
			metadata, metadataErr := provider.OAuthProtectedResourceMetadata(c.Request().Context())
			if metadataErr != nil || len(metadata.AuthorizationServers) == 0 {
				return err
			}
			teamID, teamErr := oauthRequestTeamID(c)
			if teamErr != nil {
				return err
			}
			metadataURL := oauthResourceMetadataURL(c, provider, teamID)
			if metadataURL != "" {
				c.Response().Header().Set(echo.HeaderWWWAuthenticate, fmt.Sprintf("Bearer resource_metadata=%q", metadataURL))
			}
			return err
		}
	}
}

func oauthErrorStatus(err error) int {
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		return httperr.HTTPStatusCode(apiErr.Code)
	}
	var echoErr *echo.HTTPError
	if errors.As(err, &echoErr) {
		return echoErr.Code
	}
	return nethttp.StatusInternalServerError
}

func oauthMetadataTeamID(c echo.Context) (*uuid.UUID, error) {
	raw := strings.TrimSpace(c.Param("teamId"))
	if raw == "" {
		return nil, nil
	}
	teamID, err := uuid.Parse(raw)
	if err != nil || teamID == uuid.Nil {
		return nil, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}
	return &teamID, nil
}

func oauthRequestTeamID(c echo.Context) (*uuid.UUID, error) {
	if c.Path() != "/teams/:teamId/mcp" {
		return nil, nil
	}
	return oauthMetadataTeamID(c)
}

func oauthResourceURL(c echo.Context, provider OAuthProtectedResourceProvider, teamID *uuid.UUID) string {
	base := oauthPublicBaseURL(c, provider)
	if base == "" {
		return ""
	}
	if teamID != nil {
		return base + "/teams/" + teamID.String() + "/mcp"
	}
	return base + "/mcp"
}

func oauthResourceMetadataURL(c echo.Context, provider OAuthProtectedResourceProvider, teamID *uuid.UUID) string {
	base := oauthPublicBaseURL(c, provider)
	if base == "" {
		return ""
	}
	if teamID != nil {
		return base + "/.well-known/oauth-protected-resource/teams/" + teamID.String() + "/mcp"
	}
	return base + "/.well-known/oauth-protected-resource/mcp"
}

func oauthPublicBaseURL(c echo.Context, provider OAuthProtectedResourceProvider) string {
	configured, err := provider.MCPPublicBaseURL(c.Request().Context())
	if err != nil || strings.TrimSpace(configured) == "" {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(configured), "/")
}
