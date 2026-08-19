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
		teams:         deps.TeamSvc,
		credentials:   deps.CredentialSvc,
		telemetry:     deps.Telemetry,
		graph:         deps.GraphView,
		recall:        handler.NewRecallHandler(deps.RecallSvc, deps.DreamSvc),
		dreams:        handler.NewDreamHandler(deps.DreamSvc),
		audit:         handler.NewAuditHandler(deps.AuditSvc),
		sso:           deps.SSOService,
		portal:        deps.PortalSession,
		appConfig:     deps.AppConfig,
		privateMemory: deps.PrivateMemory,
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

	authOpts := httpmw.AuthOptions{CredentialVerifier: deps.CredentialVerifier}
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
	api.POST("/session", portal.createPortalSession, httpmw.BindAndValidateStrict[dto.CreateUserPortalSessionRequest](userPortalCreateSessionBodyKey))

	teamHandler := handler.NewTeamHandler(deps.TeamSvc)
	credentialHandler := handler.NewCredentialHandler(deps.CredentialSvc)
	teamAuthz := httpmw.NewTeamAuthorizationService(deps.AuditSvc)
	teamResolutionMW := httpmw.TeamResolutionMiddleware(deps.TeamSvc)
	authorizeTeamMW := httpmw.AuthorizeTeam(teamAuthz)
	teamSvcMW := userPortalServiceAvailable(deps.TeamSvc != nil, "team service unavailable")
	credentialSvcMW := userPortalServiceAvailable(deps.CredentialSvc != nil, "credential service unavailable")
	dreamSvcMW := userPortalServiceAvailable(deps.DreamSvc != nil, "dream service unavailable")

	api.GET("/telemetry", portal.telemetrySnapshot, httpmw.RequireScopes("write"))
	api.GET("/graph", portal.graphSnapshot, httpmw.RequireScopes("read"))
	api.GET("/node-detail", portal.graphNodeDetail, httpmw.RequireScopes("read"))
	api.GET("/team/audit-log", portal.audit.Get, httpmw.RequireScopes("read"))
	api.POST("/credential/rotate", portal.rotateCurrentCredential, httpmw.RequireScopes("write"), credentialSvcMW)
	if deps.PrivateMemory != nil {
		api.DELETE("/credential/private-memory", portal.eraseCredentialPrivateMemory, httpmw.RequireScopes("write"), httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		api.GET("/private-memory/erasures/:operationId", portal.getOwnerPrivateMemoryErasure, httpmw.RequireScopes("read"))
	}
	api.GET("/team", teamHandler.Get, httpmw.RequireRole(service.CredentialRoleManager), teamSvcMW)
	api.PATCH("/team", teamHandler.Patch, httpmw.RequireRole(service.CredentialRoleManager), teamSvcMW, httpmw.BindAndValidate[dto.UpdateTeamRequest](httpmw.UpdateTeamBodyKey))
	api.DELETE("/team", teamHandler.Delete, httpmw.RequireRole(service.CredentialRoleManager), teamSvcMW)
	api.GET("/team/credentials", credentialHandler.List, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW)
	api.POST("/team/credentials", credentialHandler.Create, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW, httpmw.BindAndValidate[dto.CreateCredentialRequest](httpmw.CreateCredentialBodyKey))
	api.GET("/team/credentials/:credentialId", credentialHandler.Get, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW)
	api.PATCH("/team/credentials/:credentialId", credentialHandler.Update, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW, httpmw.BindAndValidate[dto.UpdateCredentialRequest](httpmw.UpdateCredentialBodyKey))
	api.POST("/team/credentials/:credentialId/rotate", credentialHandler.Rotate, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW, httpmw.BindAndValidate[dto.CreateCredentialRequest](httpmw.CreateCredentialBodyKey))
	api.DELETE("/team/credentials/:credentialId", credentialHandler.Delete, httpmw.RequireRole(service.CredentialRoleManager), credentialSvcMW)
	api.GET("/recall", portal.recall.Handle, teamSvcMW, teamResolutionMW, authorizeTeamMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/status", portal.dreams.Status, dreamSvcMW, teamSvcMW, teamResolutionMW, authorizeTeamMW, httpmw.RequireScopes("read"))
	api.GET("/dreaming/runs", portal.dreams.Runs, dreamSvcMW, teamSvcMW, teamResolutionMW, authorizeTeamMW, httpmw.RequireScopes("read"))
	api.GET("/dreams", portal.dreams.List, dreamSvcMW, teamSvcMW, teamResolutionMW, authorizeTeamMW, httpmw.RequireScopes("read"))
	api.GET("/dreams/:dreamId", portal.dreams.Get, dreamSvcMW, teamSvcMW, teamResolutionMW, authorizeTeamMW, httpmw.RequireScopes("read"))
	if deps.SSOService != nil {
		api.POST("/sso/team", portal.switchSSOTeam, httpmw.RequireScopes("read"))
		api.GET("/sso/credentials", portal.listSSOCredentials, httpmw.RequireScopes("read"))
		api.POST("/sso/credentials", portal.createSSOCredential, httpmw.RequireScopes("read"))
		api.GET("/sso/credentials/:credentialId", portal.getSSOCredential, httpmw.RequireScopes("read"))
		api.POST("/sso/credentials/:credentialId/rotate", portal.rotateSSOCredential, httpmw.RequireScopes("read"))
		if deps.PrivateMemory != nil {
			api.DELETE("/sso/private-memory", portal.eraseSSOPrivateMemory, httpmw.RequireScopes("write"), httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
			api.DELETE("/sso/credentials/:credentialId", portal.deleteSSOCredential, httpmw.RequireScopes("write"), httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		}
	}

	staticDir := strings.TrimSpace(deps.UserStaticDir)
	if staticDir == "" {
		staticDir = defaultUserPortalStaticDir()
	}
	registerUserPortalStatic(e, staticDir)
}

func useUserPortalMiddleware(api *echo.Group, deps UserPortalDeps, authOpts httpmw.AuthOptions) {
	api.Use(httpmw.AuthMiddlewareWithOptions(deps.CredentialRepo, deps.AuditSvc, deps.SecuritySvc, authOpts))
	api.Use(deps.ExtraMiddleware...)
	api.Use(httpmw.UsageMetricsMiddleware(deps.UsageMetrics))
	api.Use(httpmw.RateLimitMiddleware(deps.RateLimitSvc, deps.Config, deps.AuditSvc))
	api.Use(httpmw.LastUsedMiddleware(deps.LastUsedRecorder))
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
