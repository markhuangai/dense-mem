package http

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

// ProtectedDeps holds all dependencies needed for protected route registration.
// This struct collects all the middleware and service dependencies required
// for the protected MCP routes.
type ProtectedDeps struct {
	// CredentialRepo is the API key repository for authentication.
	CredentialRepo repository.CredentialRepository
	// TeamSvc resolves the authenticated team.
	TeamSvc handler.TeamServiceInterface
	// RateLimitService is the service for rate limiting.
	RateLimitService service.RateLimitServiceInterface
	// UsageMetrics records authenticated request usage for the control-panel metrics tab.
	UsageMetrics service.UsageMetricsRecorder
	// AuditService is the service for audit logging.
	AuditService service.AuditService
	// SecurityService checks active IP bans and records auth failures.
	SecurityService middleware.SecurityBanService
	// SSOAuthenticator validates SSO-linked API keys and browser SSO sessions when configured.
	SSOAuthenticator interface {
		middleware.SSOEntitlementValidator
		middleware.SSOSessionAuthenticator
	}
	// Config is the application configuration.
	Config config.ConfigProvider
	// Logger is the structured logger.
	Logger             observability.LogProvider
	CredentialVerifier crypto.CredentialVerifier
	LastUsedRecorder   middleware.LastUsedRecorder
	// PostAuthMiddleware runs after authentication, team resolution, and
	// authorization, and before usage metrics/rate limiting.
	PostAuthMiddleware []echo.MiddlewareFunc
}

// ProtectedDepsInterface is the companion interface for ProtectedDeps.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ProtectedDepsInterface interface {
	GetCredentialRepo() repository.CredentialRepository
	GetTeamSvc() handler.TeamServiceInterface
	GetRateLimitService() service.RateLimitServiceInterface
	GetUsageMetrics() service.UsageMetricsRecorder
	GetAuditService() service.AuditService
	GetSecurityService() middleware.SecurityBanService
	GetConfig() config.ConfigProvider
	GetLogger() observability.LogProvider
	GetPostAuthMiddleware() []echo.MiddlewareFunc
}

// Ensure ProtectedDeps implements ProtectedDepsInterface
var _ ProtectedDepsInterface = (*ProtectedDeps)(nil)

// Getters for ProtectedDepsInterface
func (d *ProtectedDeps) GetCredentialRepo() repository.CredentialRepository {
	return d.CredentialRepo
}

func (d *ProtectedDeps) GetTeamSvc() handler.TeamServiceInterface {
	return d.TeamSvc
}

func (d *ProtectedDeps) GetRateLimitService() service.RateLimitServiceInterface {
	return d.RateLimitService
}

func (d *ProtectedDeps) GetUsageMetrics() service.UsageMetricsRecorder {
	return d.UsageMetrics
}

func (d *ProtectedDeps) GetAuditService() service.AuditService {
	return d.AuditService
}

func (d *ProtectedDeps) GetSecurityService() middleware.SecurityBanService {
	return d.SecurityService
}

func (d *ProtectedDeps) GetConfig() config.ConfigProvider {
	return d.Config
}

func (d *ProtectedDeps) GetLogger() observability.LogProvider {
	return d.Logger
}

func (d *ProtectedDeps) GetPostAuthMiddleware() []echo.MiddlewareFunc {
	return d.PostAuthMiddleware
}

// RegisterProtectedRoutesWithHandlers registers protected API routes with the
// middleware chain required for authentication, team authorization, rate
// limiting, route-specific validation, and handler execution.
func RegisterProtectedRoutesWithHandlers(e *echo.Echo, deps ProtectedDeps, handlers ProtectedHandlers) {
	// Create team authorization service from audit service.
	teamAuthzSvc := middleware.NewTeamAuthorizationService(deps.AuditService)
	credentialAuthMW := middleware.AuthMiddlewareWithOptions(deps.CredentialRepo, deps.AuditService, deps.SecurityService, middleware.AuthOptions{
		CredentialVerifier:      deps.CredentialVerifier,
		SSOEntitlementValidator: deps.SSOAuthenticator,
	})
	usageMW := middleware.UsageMetricsMiddleware(deps.UsageMetrics)
	rateLimitMW := middleware.RateLimitMiddleware(deps.RateLimitService, deps.Config, deps.AuditService)
	lastUsedMW := middleware.LastUsedMiddleware(deps.LastUsedRecorder)
	protectedGroup := func(prefix string) *echo.Group {
		group := e.Group(prefix)
		group.Use(credentialAuthMW)
		group.Use(middleware.TeamResolutionMiddleware(deps.TeamSvc))
		group.Use(middleware.AuthorizeTeam(teamAuthzSvc))
		group.Use(deps.PostAuthMiddleware...)
		group.Use(usageMW)
		group.Use(rateLimitMW)
		group.Use(lastUsedMW)
		return group
	}

	// MCP Streamable HTTP endpoint. External memory integrations use bearer API keys only.
	mcpGroup := protectedGroup("/mcp")
	if handlers.MCPPost != nil {
		mcpGroup.POST("", handlers.MCPPost)
	}
	if handlers.MCPGet != nil {
		mcpGroup.GET("", handlers.MCPGet)
	}
}

// ProtectedHandlers holds handler functions for protected routes.
// This is provided for later units that implement real handlers.
type ProtectedHandlers struct {
	MCPPost echo.HandlerFunc
	MCPGet  echo.HandlerFunc
}
