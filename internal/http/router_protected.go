package http

import (
	"github.com/labstack/echo/v4"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

// ProtectedDeps holds all dependencies needed for protected route registration.
// This struct collects all the middleware and service dependencies required
// for the protected MCP routes.
type ProtectedDeps struct {
	// CredentialRepo is the API key repository for authentication.
	CredentialRepo accessservice.CredentialStore
	// TeamSvc resolves the authenticated team.
	TeamSvc handler.TeamServiceInterface
	// RateLimitService is the service for rate limiting.
	RateLimitService accessservice.RateLimitServiceInterface
	// UsageMetrics records authenticated request usage for retained team-overview summaries.
	UsageMetrics operations.UsageMetricsRecorder
	// AuditService is the service for audit logging.
	AuditService accessservice.AuditService
	// SecurityService checks active IP bans and records auth failures.
	SecurityService middleware.SecurityBanService
	// SSOAuthenticator validates SSO-linked API keys and browser SSO sessions when configured.
	SSOAuthenticator interface {
		middleware.SSOEntitlementValidator
		middleware.SSOSessionAuthenticator
	}
	// OAuthAuthenticator validates customer-IdP JWT bearer tokens for MCP.
	OAuthAuthenticator middleware.OAuthBearerAuthenticator
	// OAuthMetadata publishes and links the protected-resource contract.
	OAuthMetadata OAuthProtectedResourceProvider
	// Config is the application configuration.
	Config httpcontract.ConfigProvider
	// Logger is the structured logger.
	Logger                   httpcontract.LogProvider
	CredentialVerifier       httpcontract.CredentialVerifier
	CredentialLookupPrefixes httpcontract.CredentialLookupPrefixes
	LastUsedRecorder         middleware.LastUsedRecorder
	// PostAuthMiddleware runs after authentication, team resolution, and
	// authorization, and before usage metrics/rate limiting.
	PostAuthMiddleware []echo.MiddlewareFunc
}

// RegisterProtectedRoutesWithHandlers registers protected API routes with the
// middleware chain required for authentication, team authorization, rate
// limiting, route-specific validation, and handler execution.
func RegisterProtectedRoutesWithHandlers(e *echo.Echo, deps ProtectedDeps, handlers ProtectedHandlers) {
	// Create team authorization service from audit service.
	teamAuthzSvc := middleware.NewTeamAuthorizationService(deps.AuditService)
	credentialAuthMW := middleware.AuthMiddlewareWithOptions(deps.CredentialRepo, deps.AuditService, deps.SecurityService, middleware.AuthOptions{
		CredentialVerifier:       deps.CredentialVerifier,
		CredentialLookupPrefixes: deps.CredentialLookupPrefixes,
		SSOEntitlementValidator:  deps.SSOAuthenticator,
		OAuthBearerAuthenticator: deps.OAuthAuthenticator,
	})
	challengeMW := oauthProtectedResourceChallenge(deps.OAuthMetadata)
	usageMW := middleware.UsageMetricsMiddleware(deps.UsageMetrics)
	rateLimitMW := middleware.RateLimitMiddleware(deps.RateLimitService, deps.Config, deps.AuditService)
	lastUsedMW := middleware.LastUsedMiddleware(deps.LastUsedRecorder)
	protectedGroup := func(prefix string) *echo.Group {
		group := e.Group(prefix)
		group.Use(challengeMW)
		group.Use(credentialAuthMW)
		group.Use(middleware.TeamResolutionMiddleware(deps.TeamSvc))
		group.Use(middleware.AuthorizeTeam(teamAuthzSvc))
		group.Use(deps.PostAuthMiddleware...)
		group.Use(usageMW)
		group.Use(rateLimitMW)
		group.Use(lastUsedMW)
		return group
	}

	// MCP Streamable HTTP endpoints share one registry and immutable auth context.
	mcpGroup := protectedGroup("/mcp")
	if handlers.MCPPost != nil {
		mcpGroup.POST("", handlers.MCPPost)
	}
	if handlers.MCPGet != nil {
		mcpGroup.GET("", handlers.MCPGet)
	}

	scopedMCPGroup := protectedGroup("/teams/:teamId/mcp")
	if handlers.MCPPost != nil {
		scopedMCPGroup.POST("", handlers.MCPPost)
	}
	if handlers.MCPGet != nil {
		scopedMCPGroup.GET("", handlers.MCPGet)
	}
}

// ProtectedHandlers holds handler functions for protected routes.
// This is provided for later units that implement real handlers.
type ProtectedHandlers struct {
	MCPPost echo.HandlerFunc
	MCPGet  echo.HandlerFunc
}
