package http

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
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
		portal:    deps.PortalSession,
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
	portalSessionAPI := e.Group("/ui/api/session")
	portalSessionAPI.Use(publicIPRateLimitMiddleware("public-user-session", deps.RateLimitSvc, deps.Config))
	portalSessionAPI.POST("/logout", portal.logoutPortalSession)

	authOpts := httpmw.AuthOptions{}
	if deps.SSOService != nil {
		authOpts.SSOEntitlementValidator = deps.SSOService
		authOpts.SSOSessionAuthenticator = deps.SSOService
	}
	if deps.PortalSession != nil {
		authOpts.UserPortalSessionAuthenticator = deps.PortalSession
	}

	sessionAPI := e.Group("/ui/api")
	sessionAuthOpts := authOpts
	sessionAuthOpts.AllowMissingCredentials = true
	useUserPortalMiddleware(sessionAPI, deps, sessionAuthOpts)
	sessionAPI.GET("/session", portal.session)

	api := e.Group("/ui/api")
	useUserPortalMiddleware(api, deps, authOpts)
	api.POST("/session", portal.createPortalSession)

	profileHandler := handler.NewProfileHandler(deps.ProfileSvc)
	apiKeyHandler := handler.NewAPIKeyHandler(deps.APIKeySvc)
	profileAuthz := httpmw.NewProfileAuthorizationService(deps.AuditSvc)
	profileResolutionMW := httpmw.ProfileResolutionMiddleware(deps.ProfileSvc)
	authorizeProfileMW := httpmw.AuthorizeProfile(profileAuthz)
	profileSvcMW := userPortalServiceAvailable(deps.ProfileSvc != nil, "profile service unavailable")
	apiKeySvcMW := userPortalServiceAvailable(deps.APIKeySvc != nil, "api key service unavailable")
	dreamSvcMW := userPortalServiceAvailable(deps.DreamSvc != nil, "dream service unavailable")

	api.GET("/telemetry", portal.telemetrySnapshot, httpmw.RequireScopes("write"))
	api.GET("/graph", portal.graphSnapshot, httpmw.RequireScopes("read"))
	api.GET("/node-detail", portal.graphNodeDetail, httpmw.RequireScopes("read"))
	api.GET("/team/audit-log", portal.audit.Get, httpmw.RequireScopes("read"))
	api.POST("/key/rotate", portal.rotateCurrentKey, httpmw.RequireScopes("write"), apiKeySvcMW)
	api.GET("/team", profileHandler.Get, httpmw.RequireRole(service.APIKeyRoleManager), profileSvcMW)
	api.PATCH("/team", profileHandler.Patch, httpmw.RequireRole(service.APIKeyRoleManager), profileSvcMW, httpmw.BindAndValidate[dto.UpdateProfileRequest](httpmw.UpdateProfileBodyKey))
	api.DELETE("/team", profileHandler.Delete, httpmw.RequireRole(service.APIKeyRoleManager), profileSvcMW)
	api.GET("/team/profiles", apiKeyHandler.List, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW)
	api.POST("/team/profiles", apiKeyHandler.Create, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW, httpmw.BindAndValidate[dto.CreateAPIKeyRequest](httpmw.CreateAPIKeyBodyKey))
	api.GET("/team/profiles/:keyId", apiKeyHandler.Get, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW)
	api.PATCH("/team/profiles/:keyId", apiKeyHandler.Update, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW, httpmw.BindAndValidate[dto.UpdateAPIKeyRequest](httpmw.UpdateAPIKeyBodyKey))
	api.POST("/team/profiles/:keyId/rotate", apiKeyHandler.Rotate, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW, httpmw.BindAndValidate[dto.CreateAPIKeyRequest](httpmw.CreateAPIKeyBodyKey))
	api.DELETE("/team/profiles/:keyId", apiKeyHandler.Delete, httpmw.RequireRole(service.APIKeyRoleManager), apiKeySvcMW)
	api.GET("/recall", portal.recall.Handle, profileSvcMW, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/status", portal.dreams.Status, dreamSvcMW, profileSvcMW, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/runs", portal.dreams.Runs, dreamSvcMW, profileSvcMW, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreams", portal.dreams.List, dreamSvcMW, profileSvcMW, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
	api.GET("/dreams/:dreamId", portal.dreams.Get, dreamSvcMW, profileSvcMW, profileResolutionMW, authorizeProfileMW, httpmw.RequireScopes("read"))
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

func useUserPortalMiddleware(api *echo.Group, deps UserPortalDeps, authOpts httpmw.AuthOptions) {
	api.Use(httpmw.AuthMiddlewareWithOptions(deps.APIKeyRepo, deps.AuditSvc, deps.SecuritySvc, authOpts))
	api.Use(deps.ExtraMiddleware...)
	api.Use(httpmw.UsageMetricsMiddleware(deps.UsageMetrics))
	api.Use(httpmw.RateLimitMiddleware(deps.RateLimitSvc, deps.Config, deps.AuditSvc))
	api.Use(httpmw.LastUsedMiddleware(deps.APIKeyRepo))
}

func userPortalServiceAvailable(available bool, message string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !available {
				return httperr.New(httperr.SERVICE_UNAVAILABLE, message)
			}
			return next(c)
		}
	}
}
