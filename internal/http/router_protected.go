package http

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/dto"
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
	authMW := middleware.AuthMiddlewareWithSecurity(deps.APIKeyRepo, deps.AuditService, deps.SecurityService)
	usageMW := middleware.UsageMetricsMiddleware(deps.UsageMetrics)
	rateLimitMW := middleware.RateLimitMiddleware(deps.RateLimitService, deps.Config, deps.AuditService)
	lastUsedMW := middleware.LastUsedMiddleware(deps.APIKeyRepo)

	// Profile handler for profile operations
	profileHandler := handler.NewProfileHandler(deps.ProfileSvc)
	var apiKeyHandler *handler.APIKeyHandler
	if handlers.APIKeySvc != nil {
		apiKeyHandler = handler.NewAPIKeyHandler(handlers.APIKeySvc)
	}

	// ====================================
	// Team-specific routes (with :teamId in path)
	// ====================================
	// GET /api/v1/teams/:teamId → same-team
	// PATCH /api/v1/teams/:teamId → same-team + write
	auditHandler := handler.NewAuditHandler(deps.AuditService)

	registerTeamScopedRoutes := func(prefix string, legacyAPIKeyPaths bool) {
		group := e.Group(prefix)
		group.Use(authMW)
		group.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
		group.Use(middleware.AuthorizeProfile(profileAuthzSvc))
		group.Use(deps.PostAuthMiddleware...)
		group.Use(usageMW)
		group.Use(rateLimitMW)
		group.Use(lastUsedMW)

		group.GET("", profileHandler.Get, middleware.RequireScopes("read"))
		group.PATCH("", profileHandler.Patch, middleware.RequireScopes("write"), middleware.BindAndValidate[dto.UpdateProfileRequest](middleware.UpdateProfileBodyKey))

		// Audit log route (append-only, read endpoint only). The handler does
		// its own permission check for defense-in-depth.
		group.GET("/audit-log", auditHandler.Get, middleware.RequireScopes("read"))

		// Query stream SSE route. Requires Accept: text/event-stream header;
		// query = read scope.
		if handlers.QueryStream != nil {
			group.POST("/query/stream", handlers.QueryStream, middleware.RequireScopes("read"))
		}

		if apiKeyHandler == nil {
			return
		}
		if legacyAPIKeyPaths {
			group.GET("/api-keys", apiKeyHandler.List, middleware.RequireScopes("read"))
			group.POST("/api-keys", apiKeyHandler.Create, middleware.RequireScopes("write"), middleware.BindAndValidate[dto.CreateAPIKeyRequest](middleware.CreateAPIKeyBodyKey))
			group.GET("/api-keys/:keyId", apiKeyHandler.Get, middleware.RequireScopes("read"))
			group.POST("/api-keys/:keyId/rotate", apiKeyHandler.Rotate, middleware.RequireScopes("write"), middleware.BindAndValidate[dto.CreateAPIKeyRequest](middleware.CreateAPIKeyBodyKey))
			group.DELETE("/api-keys/:keyId", apiKeyHandler.Delete, middleware.RequireScopes("write"))
			return
		}
		group.GET("/profiles", apiKeyHandler.List, middleware.RequireScopes("read"))
		group.POST("/profiles", apiKeyHandler.Create, middleware.RequireScopes("write"), middleware.BindAndValidate[dto.CreateAPIKeyRequest](middleware.CreateAPIKeyBodyKey))
		group.GET("/profiles/:profileId", apiKeyHandler.Get, middleware.RequireScopes("read"))
		group.POST("/profiles/:profileId/rotate", apiKeyHandler.Rotate, middleware.RequireScopes("write"), middleware.BindAndValidate[dto.CreateAPIKeyRequest](middleware.CreateAPIKeyBodyKey))
		group.DELETE("/profiles/:profileId", apiKeyHandler.Delete, middleware.RequireScopes("write"))
	}

	registerTeamScopedRoutes("/api/v1/teams/:teamId", false)
	// Legacy aliases retained so old clients can rotate gradually.
	registerTeamScopedRoutes("/api/v1/profiles/:profileId", true)

	// Fragment routes — canonical /api/v1/fragments (AC-50)
	// Middleware: auth -> profile resolution(header) -> profile authorization -> rate limit
	fragmentGroup := e.Group("/api/v1/fragments")
	fragmentGroup.Use(authMW)
	fragmentGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	fragmentGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	fragmentGroup.Use(deps.PostAuthMiddleware...)
	fragmentGroup.Use(usageMW)
	fragmentGroup.Use(rateLimitMW)
	fragmentGroup.Use(lastUsedMW)

	if handlers.FragmentCreate != nil {
		fragmentGroup.POST("", handlers.FragmentCreate, middleware.RequireScopes("write"))
	}
	if handlers.FragmentRead != nil {
		fragmentGroup.GET("/:id", handlers.FragmentRead, middleware.RequireScopes("read"))
	}
	if handlers.FragmentList != nil {
		fragmentGroup.GET("", handlers.FragmentList, middleware.RequireScopes("read"))
	}
	if handlers.FragmentDelete != nil {
		fragmentGroup.DELETE("/:id", handlers.FragmentDelete, middleware.RequireScopes("write"))
	}
	if handlers.FragmentRetract != nil {
		fragmentGroup.POST("/:id/retract", handlers.FragmentRetract, middleware.RequireScopes("write"))
	}

	// Claim routes — canonical /api/v1/claims (AC-16, knowledge pipeline Phase 2)
	// Middleware: auth -> profile resolution(header) -> profile authorization -> rate limit
	claimGroup := e.Group("/api/v1/claims")
	claimGroup.Use(authMW)
	claimGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	claimGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	claimGroup.Use(deps.PostAuthMiddleware...)
	claimGroup.Use(usageMW)
	claimGroup.Use(rateLimitMW)
	claimGroup.Use(lastUsedMW)

	if handlers.ClaimCreate != nil {
		claimGroup.POST("", handlers.ClaimCreate, middleware.RequireScopes("write"))
	}
	if handlers.ClaimRead != nil {
		claimGroup.GET("/:id", handlers.ClaimRead, middleware.RequireScopes("read"))
	}
	if handlers.ClaimList != nil {
		claimGroup.GET("", handlers.ClaimList, middleware.RequireScopes("read"))
	}
	if handlers.ClaimDelete != nil {
		claimGroup.DELETE("/:id", handlers.ClaimDelete, middleware.RequireScopes("write"))
	}
	if handlers.ClaimVerify != nil {
		claimGroup.POST("/:id/verify", handlers.ClaimVerify, middleware.RequireScopes("write"))
	}
	if handlers.ClaimPromote != nil {
		claimGroup.POST("/:id/promote", handlers.ClaimPromote, middleware.RequireScopes("write"))
	}

	// Fact routes — canonical /api/v1/facts (AC-41, knowledge pipeline Phase 4)
	// Middleware: auth -> profile resolution(header) -> profile authorization -> rate limit
	factGroup := e.Group("/api/v1/facts")
	factGroup.Use(authMW)
	factGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	factGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	factGroup.Use(deps.PostAuthMiddleware...)
	factGroup.Use(usageMW)
	factGroup.Use(rateLimitMW)
	factGroup.Use(lastUsedMW)

	if handlers.FactGet != nil {
		factGroup.GET("/:id", handlers.FactGet, middleware.RequireScopes("read"))
	}
	if handlers.FactList != nil {
		factGroup.GET("", handlers.FactList, middleware.RequireScopes("read"))
	}
	if handlers.FactRetract != nil {
		factGroup.POST("/:id/retract", handlers.FactRetract, middleware.RequireScopes("write"))
	}

	// Community routes — canonical /api/v1/communities
	communityGroup := e.Group("/api/v1/communities")
	communityGroup.Use(authMW)
	communityGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	communityGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	communityGroup.Use(deps.PostAuthMiddleware...)
	communityGroup.Use(usageMW)
	communityGroup.Use(rateLimitMW)
	communityGroup.Use(lastUsedMW)

	if handlers.CommunityList != nil {
		communityGroup.GET("", handlers.CommunityList, middleware.RequireScopes("read"))
	}
	if handlers.CommunityRead != nil {
		communityGroup.GET("/:id", handlers.CommunityRead, middleware.RequireScopes("read"))
	}

	// MCP Streamable HTTP endpoint.
	mcpGroup := e.Group("/mcp")
	mcpGroup.Use(authMW)
	mcpGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	mcpGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	mcpGroup.Use(deps.PostAuthMiddleware...)
	mcpGroup.Use(usageMW)
	mcpGroup.Use(rateLimitMW)
	mcpGroup.Use(lastUsedMW)
	if handlers.MCPPost != nil {
		mcpGroup.POST("", handlers.MCPPost)
	}
	if handlers.MCPGet != nil {
		mcpGroup.GET("", handlers.MCPGet)
	}

	// Tool routes
	toolGroup := e.Group("/api/v1/tools")
	toolGroup.Use(authMW)
	toolGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	toolGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	toolGroup.Use(deps.PostAuthMiddleware...)
	toolGroup.Use(usageMW)
	toolGroup.Use(rateLimitMW)
	toolGroup.Use(lastUsedMW)

	if handlers.ToolCatalog != nil {
		toolGroup.GET("", handlers.ToolCatalog, middleware.RequireScopes("read"))
	}
	// Search/query tools are read-scoped (no data mutation).
	if handlers.GraphQuery != nil {
		toolGroup.POST("/graph-query", handlers.GraphQuery, middleware.RequireScopes("read"))
	}
	if handlers.KeywordSearch != nil {
		toolGroup.POST("/keyword-search", handlers.KeywordSearch, middleware.RequireScopes("read"))
	}
	if handlers.SemanticSearch != nil {
		toolGroup.POST("/semantic-search", handlers.SemanticSearch, middleware.RequireScopes("read"))
	}
	if handlers.GetTool != nil {
		toolGroup.GET("/:id", handlers.GetTool, middleware.RequireScopes("read"))
	}
	if handlers.ExecuteTool != nil {
		toolGroup.POST("/:name", handlers.ExecuteTool)
	}

	// OpenAPI — expose the full runtime contract when available. The AI-safe
	// variant remains as a fallback for reduced runtimes and tests.
	openAPIMiddleware := append([]echo.MiddlewareFunc{authMW}, deps.PostAuthMiddleware...)
	openAPIMiddleware = append(openAPIMiddleware, lastUsedMW)
	if handlers.OpenAPIFull != nil {
		e.GET("/api/v1/openapi.json", handlers.OpenAPIFull, openAPIMiddleware...)
	} else if handlers.OpenAPIAISafe != nil {
		e.GET("/api/v1/openapi.json", handlers.OpenAPIAISafe, openAPIMiddleware...)
	}

	// Recall route — canonical GET /api/v1/recall (AC-55, AC-62)
	// Middleware: auth -> profile resolution(header) -> profile authorization -> rate limit
	recallGroup := e.Group("/api/v1/recall")
	recallGroup.Use(authMW)
	recallGroup.Use(middleware.ProfileResolutionMiddleware(deps.ProfileService))
	recallGroup.Use(middleware.AuthorizeProfile(profileAuthzSvc))
	recallGroup.Use(deps.PostAuthMiddleware...)
	recallGroup.Use(usageMW)
	recallGroup.Use(rateLimitMW)
	recallGroup.Use(lastUsedMW)

	if handlers.Recall != nil {
		recallGroup.GET("", handlers.Recall, middleware.RequireScopes("read"))
	}
}

// ProtectedHandlers holds handler functions for protected routes.
// This is provided for later units that implement real handlers.
type ProtectedHandlers struct {
	ListProfiles   echo.HandlerFunc
	CreateProfile  echo.HandlerFunc
	GetProfile     echo.HandlerFunc
	UpdateProfile  echo.HandlerFunc
	DeleteProfile  echo.HandlerFunc
	GetTool        echo.HandlerFunc
	ExecuteTool    echo.HandlerFunc
	GraphQuery     echo.HandlerFunc
	KeywordSearch  echo.HandlerFunc
	SemanticSearch echo.HandlerFunc
	QueryStream    echo.HandlerFunc
	FragmentCreate echo.HandlerFunc
	FragmentRead   echo.HandlerFunc
	FragmentList   echo.HandlerFunc
	FragmentDelete echo.HandlerFunc
	ToolCatalog    echo.HandlerFunc
	OpenAPIAISafe  echo.HandlerFunc
	OpenAPIFull    echo.HandlerFunc
	MCPPost        echo.HandlerFunc
	MCPGet         echo.HandlerFunc
	APIKeySvc      handler.APIKeyServiceInterface // Service for API key routes
	// Claim handlers — knowledge pipeline Phase 2 (AC-16)
	ClaimCreate echo.HandlerFunc
	ClaimRead   echo.HandlerFunc
	ClaimList   echo.HandlerFunc
	ClaimDelete echo.HandlerFunc
	// ClaimVerify handles POST /api/v1/claims/:id/verify (Phase 3 entailment verification)
	ClaimVerify echo.HandlerFunc
	// ClaimPromote handles POST /api/v1/claims/:id/promote (Phase 4 fact promotion)
	ClaimPromote echo.HandlerFunc
	// FactGet handles GET /api/v1/facts/:id (Phase 4 fact retrieval)
	FactGet echo.HandlerFunc
	// FactList handles GET /api/v1/facts (Phase 4 fact listing)
	FactList echo.HandlerFunc
	// FactRetract handles POST /api/v1/facts/:id/retract.
	FactRetract echo.HandlerFunc
	// FragmentRetract handles POST /api/v1/fragments/:id/retract (Phase 6 soft tombstone)
	FragmentRetract echo.HandlerFunc
	// CommunityRead handles GET /api/v1/communities/:id.
	CommunityRead echo.HandlerFunc
	// CommunityList handles GET /api/v1/communities.
	CommunityList echo.HandlerFunc
	// Recall handles GET /api/v1/recall?q=...&limit=... (Phase 9 hybrid recall)
	Recall echo.HandlerFunc
}
