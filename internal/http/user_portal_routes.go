package http

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/service"
)

// RegisterUserPortal registers the API-key user portal under /ui on the main API server.
func RegisterUserPortal(e *echo.Echo, deps UserPortalDeps) {
	portal := &userPortalHandler{
		profiles:  deps.ProfileSvc,
		keys:      deps.APIKeySvc,
		telemetry: deps.Telemetry,
		graph:     deps.GraphView,
		recall:    handler.NewRecallHandler(deps.RecallSvc),
		dreams:    handler.NewDreamHandler(deps.DreamSvc),
		audit:     handler.NewAuditHandler(deps.AuditSvc),
		sso:       deps.SSOService,
		appConfig: deps.AppConfig,
	}

	if deps.SSOService != nil {
		ssoAPI := e.Group("/ui/api/sso")
		ssoAPI.Use(publicSSORateLimitMiddleware(deps.RateLimitSvc, deps.Config))
		ssoAPI.GET("/providers", portal.ssoProviders)
		ssoAPI.GET("/start/:providerId", portal.startSSO)
		ssoAPI.GET("/callback", portal.completeSSO)
		ssoAPI.POST("/logout", portal.logoutSSO)
	}

	api := e.Group("/ui/api")
	authOpts := httpmw.AuthOptions{}
	if deps.SSOService != nil {
		authOpts.SSOEntitlementValidator = deps.SSOService
		authOpts.SSOSessionAuthenticator = deps.SSOService
	}
	api.Use(httpmw.AuthMiddlewareWithOptions(deps.APIKeyRepo, deps.AuditSvc, deps.SecuritySvc, authOpts))
	api.Use(deps.ExtraMiddleware...)
	api.Use(httpmw.UsageMetricsMiddleware(deps.UsageMetrics))
	api.Use(httpmw.RateLimitMiddleware(deps.RateLimitSvc, deps.Config, deps.AuditSvc))
	api.Use(httpmw.LastUsedMiddleware(deps.APIKeyRepo))

	profileHandler := handler.NewProfileHandler(deps.ProfileSvc)
	apiKeyHandler := handler.NewAPIKeyHandler(deps.APIKeySvc)
	profileAuthz := httpmw.NewProfileAuthorizationService(deps.AuditSvc)
	profileResolutionMW := httpmw.ProfileResolutionMiddleware(deps.ProfileSvc)
	authorizeProfileMW := httpmw.AuthorizeProfile(profileAuthz)

	api.GET("/session", portal.session)
	api.GET("/telemetry", portal.telemetrySnapshot, httpmw.RequireScopes("write"))
	api.GET("/graph", portal.graphSnapshot, httpmw.RequireScopes("read"))
	api.GET("/node-detail", portal.graphNodeDetail, httpmw.RequireScopes("read"))
	api.GET("/team/audit-log", portal.audit.Get, httpmw.RequireScopes("read"))
	api.POST("/key/rotate", portal.rotateCurrentKey, httpmw.RequireScopes("write"))
	api.PATCH("/team", profileHandler.Patch, httpmw.RequireRole(service.APIKeyRoleManager), httpmw.BindAndValidate[dto.UpdateProfileRequest](httpmw.UpdateProfileBodyKey))
	api.GET("/team/profiles", apiKeyHandler.List, httpmw.RequireRole(service.APIKeyRoleManager))
	api.POST("/team/profiles", apiKeyHandler.Create, httpmw.RequireRole(service.APIKeyRoleManager), httpmw.BindAndValidate[dto.CreateAPIKeyRequest](httpmw.CreateAPIKeyBodyKey))
	api.PATCH("/team/profiles/:profileId", apiKeyHandler.Update, httpmw.RequireRole(service.APIKeyRoleManager), httpmw.BindAndValidate[dto.UpdateAPIKeyRequest](httpmw.UpdateAPIKeyBodyKey))
	api.POST("/team/profiles/:profileId/rotate", apiKeyHandler.Rotate, httpmw.RequireRole(service.APIKeyRoleManager), httpmw.BindAndValidate[dto.CreateAPIKeyRequest](httpmw.CreateAPIKeyBodyKey))
	api.DELETE("/team/profiles/:profileId", apiKeyHandler.Delete, httpmw.RequireRole(service.APIKeyRoleManager))
	api.GET("/recall", portal.recall.Handle, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/status", portal.dreams.Status, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/runs", portal.dreams.Runs, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreams", portal.dreams.List, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	if deps.SSOService != nil {
		api.POST("/sso/team", portal.switchSSOTeam, httpmw.RequireScopes("read"))
		api.POST("/sso/key", portal.createSSOKey, httpmw.RequireScopes("read"))
		api.POST("/sso/key/rotate", portal.rotateSSOKey, httpmw.RequireScopes("read"))
	}

	staticDir := strings.TrimSpace(deps.UserStaticDir)
	if staticDir == "" {
		staticDir = defaultUserPortalStaticDir()
	}
	registerUserPortalStatic(e, staticDir)
}
