package http

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

// ProtectedDeps holds all dependencies needed for protected route registration.
// This struct collects all the middleware and service dependencies required
// for the protected route groups (profile and tool/data routes).
type ProtectedDeps struct {
	// APIKeyRepo is the API key repository for authentication.
	APIKeyRepo repository.APIKeyRepository
	// ProfileService is the service for profile resolution and authorization.
	ProfileService middleware.ProfileResolutionServiceInterface
	// ProfileSvc is the service for profile CRUD operations (used by handlers).
	ProfileSvc handler.ProfileServiceInterface
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
	Logger observability.LogProvider
	// PostAuthMiddleware runs after authentication, profile resolution, and
	// authorization, and before usage metrics/rate limiting.
	PostAuthMiddleware []echo.MiddlewareFunc
}

// ProtectedDepsInterface is the companion interface for ProtectedDeps.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ProtectedDepsInterface interface {
	GetAPIKeyRepo() repository.APIKeyRepository
	GetProfileService() middleware.ProfileResolutionServiceInterface
	GetProfileSvc() handler.ProfileServiceInterface
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
func (d *ProtectedDeps) GetAPIKeyRepo() repository.APIKeyRepository {
	return d.APIKeyRepo
}

func (d *ProtectedDeps) GetProfileService() middleware.ProfileResolutionServiceInterface {
	return d.ProfileService
}

func (d *ProtectedDeps) GetProfileSvc() handler.ProfileServiceInterface {
	return d.ProfileSvc
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
// middleware chain required for authentication, profile authorization, rate
// limiting, route-specific validation, and handler execution.
func RegisterProtectedRoutesWithHandlers(e *echo.Echo, deps ProtectedDeps, handlers ProtectedHandlers) {
	// Create profile authorization service from audit service
	profileAuthzSvc := middleware.NewProfileAuthorizationService(deps.AuditService)
	apiKeyAuthMW := middleware.AuthMiddlewareWithOptions(deps.APIKeyRepo, deps.AuditService, deps.SecurityService, middleware.AuthOptions{
		SSOEntitlementValidator: deps.SSOAuthenticator,
	})
	usageMW := middleware.UsageMetricsMiddleware(deps.UsageMetrics)
	rateLimitMW := middleware.RateLimitMiddleware(deps.RateLimitService, deps.Config, deps.AuditService)
	lastUsedMW := middleware.LastUsedMiddleware(deps.APIKeyRepo)
	protectedGroup := func(prefix string) *echo.Group {
		group := e.Group(prefix)
		group.Use(apiKeyAuthMW)
		group.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
		group.Use(middleware.AuthorizeProfile(profileAuthzSvc))
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
