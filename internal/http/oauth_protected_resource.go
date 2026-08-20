package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

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

const (
	oauthProtectedResourceMetadataPrefix        = "/.well-known/oauth-protected-resource"
	oauthProtectedResourceMetadataWildcardRoute = oauthProtectedResourceMetadataPrefix + "/*"
)

func RegisterOAuthProtectedResourceRoutes(e *echo.Echo, provider OAuthProtectedResourceProvider) {
	if e == nil || provider == nil {
		return
	}
	handler := oauthProtectedResourceMetadataHandler(provider)
	e.GET(oauthProtectedResourceMetadataPrefix+"/mcp", handler)
	e.GET(oauthProtectedResourceMetadataPrefix+"/teams/:teamId/mcp", handler)
	e.GET(oauthProtectedResourceMetadataWildcardRoute, handler)
}

func oauthProtectedResourceMetadataHandler(provider OAuthProtectedResourceProvider) echo.HandlerFunc {
	return func(c echo.Context) error {
		metadata, err := provider.OAuthProtectedResourceMetadata(c.Request().Context())
		if err != nil {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "oauth resource metadata unavailable")
		}
		if len(metadata.AuthorizationServers) == 0 {
			return httperr.New(httperr.NOT_FOUND, "oauth protected resource is not configured")
		}
		base := oauthPublicBaseURL(c, provider)
		if base == nil {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "oauth resource metadata unavailable")
		}
		teamID, err := oauthMetadataTeamID(c, base.Path)
		if err != nil {
			return err
		}
		resourceURL, metadataURL := oauthResourceURLs(base, teamID)
		if c.Path() == oauthProtectedResourceMetadataWildcardRoute && c.Request().URL.Path != metadataURL.Path {
			return httperr.New(httperr.NOT_FOUND, "oauth protected resource metadata not found")
		}
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		return c.JSON(nethttp.StatusOK, oauthProtectedResourceDocument{
			Resource:             resourceURL.String(),
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
			base := oauthPublicBaseURL(c, provider)
			if base != nil {
				_, metadataURL := oauthResourceURLs(base, teamID)
				c.Response().Header().Set(echo.HeaderWWWAuthenticate, fmt.Sprintf("Bearer resource_metadata=%q", metadataURL.String()))
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

func oauthMetadataTeamID(c echo.Context, basePath string) (*uuid.UUID, error) {
	if c.Path() != oauthProtectedResourceMetadataWildcardRoute {
		return oauthTeamIDParam(c)
	}
	requestPath := c.Request().URL.Path
	configuredPrefix := oauthProtectedResourceMetadataPrefix + strings.TrimSuffix(basePath, "/")
	relative, ok := strings.CutPrefix(requestPath, configuredPrefix)
	if !ok {
		return nil, httperr.New(httperr.NOT_FOUND, "oauth protected resource metadata not found")
	}
	if relative == "/mcp" {
		return nil, nil
	}
	parts := strings.Split(strings.TrimPrefix(relative, "/"), "/")
	if len(parts) != 3 || parts[0] != "teams" || parts[2] != "mcp" {
		return nil, httperr.New(httperr.NOT_FOUND, "oauth protected resource metadata not found")
	}
	teamID, err := uuid.Parse(parts[1])
	if err != nil || teamID == uuid.Nil {
		return nil, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}
	return &teamID, nil
}

func oauthTeamIDParam(c echo.Context) (*uuid.UUID, error) {
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
	return oauthTeamIDParam(c)
}

func oauthResourceURLs(base *url.URL, teamID *uuid.UUID) (*url.URL, *url.URL) {
	resourcePath := strings.TrimSuffix(base.Path, "/")
	if teamID != nil {
		resourcePath += "/teams/" + teamID.String() + "/mcp"
	} else {
		resourcePath += "/mcp"
	}

	resourceURL := *base
	resourceURL.Path = resourcePath
	resourceURL.RawPath = ""
	metadataURL := *base
	metadataURL.Path = oauthProtectedResourceMetadataPrefix + resourcePath
	metadataURL.RawPath = ""
	return &resourceURL, &metadataURL
}

func oauthPublicBaseURL(c echo.Context, provider OAuthProtectedResourceProvider) *url.URL {
	configured, err := provider.MCPPublicBaseURL(c.Request().Context())
	if err != nil || strings.TrimSpace(configured) == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(configured))
	if err != nil || !oauthPublicBaseSchemeAllowed(parsed) || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawPath != "" {
		return nil
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed
}

func oauthPublicBaseSchemeAllowed(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
