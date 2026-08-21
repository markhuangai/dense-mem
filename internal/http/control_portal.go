package http

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	httpvalidation "github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/tools"
)

// NewControlPortalServer creates the token-protected management portal server.
func NewControlPortalServer(
	cfg config.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetrics(cfg, teamSvc, credentialSvc, nil, HealthConfig{}, logger, securitySvcs...)
}

func NewControlPortalServerWithMetrics(
	cfg config.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc service.UsageMetricsReader,
	health HealthConfig,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetricsAndTelemetry(
		cfg,
		teamSvc,
		credentialSvc,
		metricsSvc,
		ControlPortalTelemetry{},
		health,
		logger,
		securitySvcs...,
	)
}

func NewControlPortalServerWithMetricsAndTelemetry(
	cfg config.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc service.UsageMetricsReader,
	telemetry ControlPortalTelemetry,
	health HealthConfig,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("control portal: config is required")
	}
	if health.dependencyFlights == nil {
		health.dependencyFlights = newDependencyCheckFlightRegistry()
	}
	if strings.TrimSpace(cfg.GetControlPortalToken()) == "" {
		return nil, fmt.Errorf("control portal: token is required")
	}

	e := echo.New()
	applyServerLimits(e)
	applyIPExtractor(e)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(echomw.Recover())
	e.Use(echomw.BodyLimit(fmt.Sprintf("%dB", controlMaxBodyBytes(cfg))))
	e.Use(httpmw.CorrelationIDMiddleware())
	e.Use(echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		HandleError: true,
		LogMethod:   true,
		LogURI:      true,
		LogStatus:   true,
		LogValuesFunc: func(_ echo.Context, v echomw.RequestLoggerValues) error {
			if logger == nil {
				return nil
			}
			attrs := []observability.LogAttr{
				observability.String("method", v.Method),
				observability.String("uri", v.URI),
				observability.Int("status", v.Status),
			}
			if v.Error != nil {
				logger.Error("control_http_request", errors.New(tools.SanitizeError(v.Error)), attrs...)
				return nil
			}
			logger.Info("control_http_request", attrs...)
			return nil
		},
	}))

	var securitySvc service.SecurityService
	if len(securitySvcs) > 0 {
		securitySvc = securitySvcs[0]
	}
	if securitySvc != nil {
		e.Use(httpmw.SecurityBanMiddleware(securitySvc))
	}

	if telemetry.ScrapeHandler != nil {
		if strings.TrimSpace(telemetry.ScrapeToken) == "" {
			return nil, fmt.Errorf("control portal: telemetry scrape token is required")
		}
		e.GET("/metrics", echo.WrapHandler(telemetry.ScrapeHandler), httpmw.TelemetryScrapeTokenMiddleware(telemetry.ScrapeToken))
	}

	control := &controlPortalHandler{teams: teamSvc, credentials: credentialSvc, security: securitySvc, metrics: metricsSvc, telemetry: telemetry.Reader, operationLogs: telemetry.Logs, recallFeedback: telemetry.RecallFeedback, dreams: telemetry.Dreams, communities: telemetry.Communities, conflictQueue: telemetry.ConflictQueue, convergence: telemetry.Convergence, submissions: telemetry.Submissions, privateMemory: telemetry.PrivateMemory, health: health, sso: telemetry.SSO, directory: telemetry.Directory, controlIdentity: telemetry.ControlIdentity, appConfig: telemetry.Config, logger: logger, verifierModel: cfg.GetAIVerifierModel(), embeddingModel: cfg.GetAIEmbeddingModel()}
	if telemetry.ControlIdentity != nil {
		registerControlIdentityRoutes(e, control)
	}
	api := e.Group("/control/api")
	api.Use(controlPortalMiddleware(cfg.GetControlPortalToken(), securitySvc, telemetry.ControlIdentity))
	api.Use(httpmw.TelemetryHTTPMiddleware(telemetry.HTTPMetrics))
	api.GET("/session", control.session)
	api.GET("/metrics", control.getMetrics)
	api.GET("/telemetry", control.getTelemetry)
	if telemetry.Convergence != nil {
		api.GET("/search/convergence", control.getSearchConvergence)
	}
	if telemetry.Logs != nil {
		api.GET("/logs", control.listOperationLogs)
	}
	if telemetry.Submissions != nil {
		api.GET("/submissions", control.listSubmissionDiagnostics)
		api.GET("/teams/:teamId/submissions/:submissionId", control.getSubmissionDiagnostic)
	}
	if telemetry.RecallFeedback != nil {
		api.GET("/recall-feedback-events", control.listRecallFeedbackEvents)
		api.GET("/recall-feedback-events/:recallId", control.getRecallFeedbackEvent)
	}
	api.GET("/teams", control.listTeams)
	api.POST("/teams", control.createTeam)
	api.PATCH("/teams/:teamId", control.updateTeam)
	api.DELETE("/teams/:teamId", control.deleteTeam)
	if telemetry.Dreams != nil {
		api.GET("/teams/:teamId/dreaming/status", control.getTeamDreamingStatus)
		api.GET("/teams/:teamId/dreaming/runs", control.listTeamDreamingRuns)
		api.GET("/teams/:teamId/dreams", control.listTeamDreams)
		api.GET("/teams/:teamId/dreams/:dreamId", control.getTeamDream)
	}
	if telemetry.Communities != nil {
		api.GET("/teams/:teamId/community/status", control.getTeamCommunityStatus)
	}
	if telemetry.ConflictQueue != nil {
		api.GET("/teams/:teamId/conflicts/queue", control.listConflictQueue)
	}
	api.GET("/teams/:teamId/credentials", control.listCredentials)
	api.POST("/teams/:teamId/credentials", control.createCredential)
	api.PATCH("/teams/:teamId/credentials/:credentialId", control.updateCredential)
	api.POST("/teams/:teamId/credentials/:credentialId/rotate", control.rotateCredential)
	api.DELETE("/teams/:teamId/credentials/:credentialId", control.deleteCredential)
	api.GET("/security/settings", control.getSecuritySettings)
	api.PATCH("/security/settings", control.updateSecuritySettings)
	api.GET("/security/bans", control.listSecurityBans)
	api.POST("/security/bans", control.createSecurityBan)
	api.DELETE("/security/bans/:ip", control.deleteSecurityBan)
	if telemetry.Config != nil {
		api.GET("/config/general", control.getGeneralConfig)
		api.PATCH("/config/general", control.updateGeneralConfig)
		api.GET("/config/sso", control.getSSOConfig)
		api.PATCH("/config/sso", control.updateSSOConfig)
		api.GET("/config/dreaming", control.getDreamingConfig)
		api.PATCH("/config/dreaming", control.updateDreamingConfig)
		api.GET("/config/community-detection", control.getCommunityDetectionConfig)
		api.PATCH("/config/community-detection", control.updateCommunityDetectionConfig)
		api.GET("/config/operation-logs", control.getOperationLogConfig)
		api.PATCH("/config/operation-logs", control.updateOperationLogConfig)
		api.GET("/config/recall-feedback", control.getRecallFeedbackConfig)
		api.PATCH("/config/recall-feedback", control.updateRecallFeedbackConfig)
		api.GET("/config/telemetry-pricing", control.getTelemetryPricingConfig)
		api.PATCH("/config/telemetry-pricing", control.updateTelemetryPricingConfig)
		api.GET("/config/private-memory", control.getPrivateMemoryConfig)
		api.PATCH("/config/private-memory", control.updatePrivateMemoryConfig, httpmw.BindAndValidateStrict[controlPrivateMemoryConfigRequest](privateMemoryConfigBodyKey))
	}
	if telemetry.PrivateMemory != nil {
		api.GET("/private-memory/spaces", control.listPrivateMemorySpaces)
		api.POST("/private-memory/spaces/:spaceId/legal-hold", control.placePrivateMemoryLegalHold, httpmw.BindAndValidateStrict[dto.PrivateMemoryLegalHoldRequest](privateMemoryLegalHoldBodyKey))
		api.DELETE("/private-memory/spaces/:spaceId/legal-hold", control.releasePrivateMemoryLegalHold)
		api.POST("/private-memory/spaces/:spaceId/erasures", control.requestControlPrivateMemoryErasure, httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		api.GET("/private-memory/erasures", control.listPrivateMemoryErasures)
		api.GET("/private-memory/erasures/:operationId", control.getPrivateMemoryErasure)
		api.POST("/private-memory/retention-runs", control.runPrivateMemoryRetention, httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		api.GET("/private-memory/retention-runs", control.listPrivateMemoryRetentionRuns)
	}
	if telemetry.SSO != nil {
		api.GET("/sso/providers", control.listSSOProviders)
		api.POST("/sso/providers", control.createSSOProvider)
		api.PATCH("/sso/providers/:providerId", control.updateSSOProvider)
		api.DELETE("/sso/providers/:providerId", control.deleteSSOProvider)
		api.GET("/sso/providers/:providerId/mappings", control.listSSOMappings)
		api.POST("/sso/providers/:providerId/mappings", control.createSSOMapping)
		api.PATCH("/sso/providers/:providerId/mappings/:mappingId", control.updateSSOMapping)
		api.DELETE("/sso/providers/:providerId/mappings/:mappingId", control.deleteSSOMapping)
	}
	if telemetry.Directory != nil {
		api.GET("/sso/directory/connectors", control.listDirectoryConnectors)
		api.GET("/sso/providers/:providerId/directory-connector", control.getDirectoryConnector)
		api.POST("/sso/providers/:providerId/directory-connector", control.createDirectoryConnector)
		api.PATCH("/sso/directory/connectors/:connectorId", control.updateDirectoryConnector)
		api.POST("/sso/directory/connectors/:connectorId/credentials/rotate", control.rotateDirectoryCredentials)
		api.GET("/sso/directory/connectors/:connectorId/preview", control.previewDirectoryConnector)
		api.POST("/sso/directory/connectors/:connectorId/status", control.setDirectoryConnectorStatus)
		api.POST("/sso/directory/connectors/:connectorId/groups/:groupId/adopt", control.adoptDirectoryGroupTeam)
	}
	if telemetry.ControlIdentity != nil {
		api.GET("/sso/providers/:providerId/control-admin-groups", control.listControlAdminGroups)
		api.POST("/sso/providers/:providerId/control-admin-groups", control.createControlAdminGroup)
		api.PATCH("/sso/providers/:providerId/control-admin-groups/:groupId", control.updateControlAdminGroup)
		api.DELETE("/sso/providers/:providerId/control-admin-groups/:groupId", control.deleteControlAdminGroup)
	}

	if staticDir := defaultPortalStaticDir(); staticDir != "" {
		e.Static("/", staticDir)
	}

	return e, nil
}

func (h *controlPortalHandler) getMetrics(c echo.Context) error {
	if h.metrics == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "usage metrics unavailable")
	}
	filter, err := controlMetricsFilter(c)
	if err != nil {
		return err
	}
	snapshot, err := h.metrics.Snapshot(c.Request().Context(), filter)
	if err != nil {
		return err
	}
	dependencyReport := observeDependencies(c.Request().Context(), h.health)
	return c.JSON(nethttp.StatusOK, map[string]any{"data": controlMetricsResponse{
		Window:                snapshot.Window,
		System:                snapshot.System,
		DependenciesCheckedAt: dependencyReport.CheckedAt,
		Dependencies:          dependencyReport.Dependencies,
		Teams:                 snapshot.Teams,
		Keys:                  snapshot.Keys,
		Routes:                snapshot.Routes,
	}})
}

func (h *controlPortalHandler) getTelemetry(c echo.Context) error {
	if h.telemetry == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "telemetry unavailable")
	}
	filter, err := controlTelemetryFilter(c)
	if err != nil {
		return err
	}
	snapshot, err := h.telemetry.Snapshot(c.Request().Context(), filter)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": snapshot})
}

func (h *controlPortalHandler) listTeams(c echo.Context) error {
	limit, offset := controlPagination(c)
	teams, err := h.teams.List(c.Request().Context(), limit, offset)
	if err != nil {
		return err
	}
	total, err := h.teams.Count(c.Request().Context())
	if err != nil {
		return err
	}
	items := make([]controlTeamResponse, 0, len(teams))
	for _, team := range teams {
		item, err := h.toControlTeam(c.Request().Context(), team)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data:       items,
		Pagination: handler.Pagination{Limit: limit, Offset: offset, Total: total},
	})
}

func (h *controlPortalHandler) createTeam(c echo.Context) error {
	var body dto.CreateTeamRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if err := httpvalidation.ValidateStruct(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}
	team, err := h.teams.Create(c.Request().Context(), service.CreateTeamRequest{
		Name:        body.Name,
		Description: body.Description,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	item, err := h.toControlTeam(c.Request().Context(), team)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": item})
}

func (h *controlPortalHandler) updateTeam(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	var body dto.UpdateTeamRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if err := httpvalidation.ValidateStruct(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}
	var namePtr, descPtr *string
	if body.Name != "" {
		namePtr = &body.Name
	}
	if body.Description != "" {
		descPtr = &body.Description
	}
	team, err := h.teams.Update(c.Request().Context(), teamID, service.UpdateTeamRequest{
		Name:        namePtr,
		Description: descPtr,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	item, err := h.toControlTeam(c.Request().Context(), team)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": item})
}

func (h *controlPortalHandler) deleteTeam(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	if err := h.teams.Delete(c.Request().Context(), teamID, nil, "control", c.RealIP(), ""); err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

func (h *controlPortalHandler) listCredentials(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	limit, offset := controlPagination(c)
	credentials, err := h.credentials.ListByTeam(c.Request().Context(), teamID, limit, offset)
	if err != nil {
		return err
	}
	total, err := h.credentials.CountByTeam(c.Request().Context(), teamID)
	if err != nil {
		return err
	}
	items := make([]controlCredentialResponse, 0, len(credentials))
	for _, credential := range credentials {
		items = append(items, toControlCredential(credential))
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data:       items,
		Pagination: handler.Pagination{Limit: limit, Offset: offset, Total: total},
	})
}

func (h *controlPortalHandler) createCredential(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	var body controlCreateCredentialRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	req := service.CreateCredentialRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
		Role:      body.Role,
	}
	if body.MemoryBinding != nil {
		req.MemoryBinding = *body.MemoryBinding
	}
	if body.Scopes != nil {
		if len(*body.Scopes) == 0 {
			return httperr.New(httperr.VALIDATION_ERROR, service.CredentialScopeValidationMessage())
		}
		req.Scopes = append([]string(nil), (*body.Scopes)...)
	}
	if body.ExpiresAt != nil {
		expiresAt, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "expires_at must be RFC3339")
		}
		req.ExpiresAt = &expiresAt
	}
	credential, rawKey, err := h.credentials.CreateCredential(c.Request().Context(), teamID, req, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{
		"data": map[string]any{
			"api_key":    rawKey,
			"credential": toControlCredential(credential),
		},
	})
}

func (h *controlPortalHandler) updateCredential(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	credentialID, err := parseControlUUID(controlCredentialIDParam(c), "credential ID")
	if err != nil {
		return err
	}
	var body controlUpdateCredentialRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	namePresent := strings.TrimSpace(body.Name) != ""
	rolePresent := strings.TrimSpace(body.Role) != ""
	scopesPresent := body.Scopes != nil
	if !namePresent && !rolePresent && !scopesPresent {
		return httperr.New(httperr.VALIDATION_ERROR, "credential name, role, or scopes is required")
	}
	if boolCount(namePresent, rolePresent, scopesPresent) > 1 {
		return httperr.New(httperr.VALIDATION_ERROR, "credential name, role, and scopes must be updated separately")
	}
	if rolePresent {
		if _, err := service.NormalizeCredentialRole(body.Role); err != nil {
			return err
		}
	}
	if scopesPresent && len(*body.Scopes) == 0 {
		return httperr.New(httperr.VALIDATION_ERROR, service.CredentialScopeValidationMessage())
	}
	var credential *domain.Credential
	if namePresent {
		credential, err = h.credentials.UpdateNameForTeam(c.Request().Context(), teamID, credentialID, body.Name, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	if rolePresent {
		credential, err = h.credentials.UpdateRoleForTeam(c.Request().Context(), teamID, credentialID, body.Role, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	if scopesPresent {
		credential, err = h.credentials.UpdateScopesForTeam(c.Request().Context(), teamID, credentialID, *body.Scopes, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlCredential(credential)})
}

func (h *controlPortalHandler) rotateCredential(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	credentialID, err := parseControlUUID(controlCredentialIDParam(c), "credential ID")
	if err != nil {
		return err
	}
	var body controlCreateCredentialRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if body.Scopes != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "scopes cannot be changed by rotating a key")
	}
	if strings.TrimSpace(body.Role) != "" {
		return httperr.New(httperr.VALIDATION_ERROR, "role cannot be changed by rotating a key")
	}
	req := service.CreateCredentialRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
	}
	if body.ExpiresAt != nil {
		expiresAt, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "expires_at must be RFC3339")
		}
		req.ExpiresAt = &expiresAt
	}
	credential, rawKey, err := h.credentials.RotateForTeam(c.Request().Context(), teamID, credentialID, req, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{
		"data": map[string]any{
			"api_key":    rawKey,
			"credential": toControlCredential(credential),
		},
	})
}

func (h *controlPortalHandler) deleteCredential(c echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	credentialID, err := parseControlUUID(controlCredentialIDParam(c), "credential ID")
	if err != nil {
		return err
	}
	if err := h.credentials.DeleteForTeam(c.Request().Context(), teamID, credentialID, nil, "control", c.RealIP(), ""); err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

func (h *controlPortalHandler) getSecuritySettings(c echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	settings, err := h.security.GetSecuritySettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSecuritySettings(settings)})
}

func (h *controlPortalHandler) updateSecuritySettings(c echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	current, err := h.security.GetSecuritySettings(c.Request().Context())
	if err != nil {
		return err
	}
	next := *current
	var body controlSecuritySettingsRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}
	if body.FailureThreshold != nil {
		next.FailureThreshold = *body.FailureThreshold
	}
	if body.FailureWindowSeconds != nil {
		next.FailureWindowSeconds = *body.FailureWindowSeconds
	}
	if body.BanDurationSeconds != nil {
		next.BanDurationSeconds = *body.BanDurationSeconds
	}
	updated, err := h.security.UpdateSecuritySettings(c.Request().Context(), next, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidSecuritySettings) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSecuritySettings(updated)})
}

func (h *controlPortalHandler) listSecurityBans(c echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	limit, offset := controlPagination(c)
	includeExpired := parseControlBool(c.QueryParam("include_expired"))
	bans, total, err := h.security.ListSecurityBans(c.Request().Context(), includeExpired, limit, offset)
	if err != nil {
		return err
	}
	items := make([]controlSecurityBanResponse, 0, len(bans))
	for _, ban := range bans {
		items = append(items, toControlSecurityBan(ban))
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data:       items,
		Pagination: handler.Pagination{Limit: limit, Offset: offset, Total: total},
	})
}

func (h *controlPortalHandler) createSecurityBan(c echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	var body controlCreateSecurityBanRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil && strings.TrimSpace(*body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "expires_at must be RFC3339")
		}
		expiresAt = &parsed
	}
	ban, err := h.security.CreateManualSecurityBan(c.Request().Context(), body.IP, body.Reason, expiresAt, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidSecurityIP) || errors.Is(err, service.ErrInvalidSecuritySettings) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": toControlSecurityBan(*ban)})
}

func (h *controlPortalHandler) deleteSecurityBan(c echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	ip := c.Param("ip")
	if err := h.security.DeleteSecurityBan(c.Request().Context(), ip, "control", c.RealIP(), ""); err != nil {
		if errors.Is(err, service.ErrInvalidSecurityIP) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

type controlCreateCredentialRequest struct {
	Name          string    `json:"name"`
	Scopes        *[]string `json:"scopes"`
	Role          string    `json:"role"`
	RateLimit     int       `json:"rate_limit"`
	ExpiresAt     *string   `json:"expires_at"`
	MemoryBinding *string   `json:"memory_binding"`
}

type controlUpdateCredentialRequest struct {
	Name   string    `json:"name"`
	Role   string    `json:"role"`
	Scopes *[]string `json:"scopes"`
}

type controlSecuritySettingsRequest struct {
	Enabled              *bool `json:"enabled"`
	FailureThreshold     *int  `json:"failure_threshold"`
	FailureWindowSeconds *int  `json:"failure_window_seconds"`
	BanDurationSeconds   *int  `json:"ban_duration_seconds"`
}

type controlCreateSecurityBanRequest struct {
	IP        string  `json:"ip"`
	Reason    string  `json:"reason"`
	ExpiresAt *string `json:"expires_at"`
}

type controlMetricsResponse struct {
	Window                domain.UsageMetricsWindow   `json:"window"`
	System                domain.UsageMetricTotal     `json:"system"`
	DependenciesCheckedAt string                      `json:"dependencies_checked_at"`
	Dependencies          []controlDependencyResponse `json:"dependencies"`
	Teams                 []domain.UsageTeamMetric    `json:"teams"`
	Keys                  []domain.UsageKeyMetric     `json:"keys"`
	Routes                []domain.UsageRouteMetric   `json:"routes"`
}

type controlDependencyResponse struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	LatencyMS  *int64  `json:"latency_ms"`
	Message    *string `json:"message,omitempty"`
	ReasonCode *string `json:"reason_code,omitempty"`
}

const (
	controlMetricsDefaultWindowMinutes = 60
	controlMetricsMaxWindowMinutes     = 43200
)

func controlMetricsFilter(c echo.Context) (domain.UsageMetricsFilter, error) {
	windowMinutes := controlMetricsDefaultWindowMinutes
	if raw := strings.TrimSpace(c.QueryParam("window_minutes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > controlMetricsMaxWindowMinutes {
			return domain.UsageMetricsFilter{}, httperr.New(httperr.VALIDATION_ERROR, "window_minutes must be between 1 and 43200")
		}
		windowMinutes = parsed
	}

	var teamID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return domain.UsageMetricsFilter{}, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
		}
		teamID = &parsed
	}

	to := time.Now().UTC()
	return domain.UsageMetricsFilter{
		From:   to.Add(-time.Duration(windowMinutes) * time.Minute),
		To:     to,
		TeamID: teamID,
	}, nil
}

func controlTelemetryFilter(c echo.Context) (service.TelemetryFilter, error) {
	scope := strings.TrimSpace(c.QueryParam("scope"))
	if scope == "" {
		scope = "system"
	}
	switch scope {
	case "system", "team", "profile":
	default:
		return service.TelemetryFilter{}, httperr.New(httperr.VALIDATION_ERROR, "scope must be one of system, team, profile")
	}

	var teamID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return service.TelemetryFilter{}, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
		}
		teamID = &parsed
	}

	var profileID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("profile_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return service.TelemetryFilter{}, httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
		}
		profileID = &parsed
	}

	return service.TelemetryFilter{
		Window:    strings.TrimSpace(c.QueryParam("window")),
		Scope:     scope,
		TeamID:    teamID,
		ProfileID: profileID,
		Audience:  service.TelemetryAudienceOperator,
	}, nil
}

type controlBodyLimitConfig interface {
	GetHTTPMaxBodyBytes() int
}

func controlMaxBodyBytes(cfg config.ConfigProvider) int {
	if provider, ok := cfg.(controlBodyLimitConfig); ok {
		return effectiveMaxBodyBytes(provider.GetHTTPMaxBodyBytes())
	}
	return effectiveMaxBodyBytes(0)
}

func controlTokenMatches(req *nethttp.Request, expected string) bool {
	got := req.Header.Get("X-Control-Portal-Token")
	if got == "" {
		auth := req.Header.Get(echo.HeaderAuthorization)
		if strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func defaultPortalStaticDir() string {
	candidates := []string{
		filepath.Join("web", "dist"),
		filepath.Join("/app", "dense-mem", "web", "dist"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func parseControlUUID(raw, label string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, httperr.New(httperr.VALIDATION_ERROR, label+" is required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, httperr.New(httperr.INVALID_UUID, "invalid "+label+" format")
	}
	return id, nil
}
func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
func controlTeamIDParam(c echo.Context) string {
	return c.Param("teamId")
}

func controlCredentialIDParam(c echo.Context) string {
	return c.Param("credentialId")
}

func controlPagination(c echo.Context) (int, int) {
	limit := 20
	offset := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			if parsed > 100 {
				parsed = 100
			}
			limit = parsed
		}
	}
	if raw := c.QueryParam("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

func parseControlBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type controlSecuritySettingsResponse struct {
	Enabled              bool   `json:"enabled"`
	FailureThreshold     int    `json:"failure_threshold"`
	FailureWindowSeconds int    `json:"failure_window_seconds"`
	BanDurationSeconds   int    `json:"ban_duration_seconds"`
	UpdatedAt            string `json:"updated_at"`
}

func toControlSecuritySettings(settings *domain.SecuritySettings) controlSecuritySettingsResponse {
	if settings == nil {
		return controlSecuritySettingsResponse{}
	}
	return controlSecuritySettingsResponse{
		Enabled:              settings.Enabled,
		FailureThreshold:     settings.FailureThreshold,
		FailureWindowSeconds: settings.FailureWindowSeconds,
		BanDurationSeconds:   settings.BanDurationSeconds,
		UpdatedAt:            settings.UpdatedAt.Format(time.RFC3339),
	}
}

type controlSecurityBanResponse struct {
	IP           string         `json:"ip"`
	Reason       string         `json:"reason"`
	Source       string         `json:"source"`
	FailureCount int            `json:"failure_count"`
	BannedAt     string         `json:"banned_at"`
	ExpiresAt    *string        `json:"expires_at"`
	LastFailedAt *string        `json:"last_failed_at"`
	Metadata     map[string]any `json:"metadata"`
	RevokedAt    *string        `json:"revoked_at"`
}

func toControlSecurityBan(ban domain.SecurityIPBan) controlSecurityBanResponse {
	return controlSecurityBanResponse{
		IP:           ban.IP,
		Reason:       ban.Reason,
		Source:       ban.Source,
		FailureCount: ban.FailureCount,
		BannedAt:     ban.BannedAt.Format(time.RFC3339),
		ExpiresAt:    controlTimePtr(ban.ExpiresAt),
		LastFailedAt: controlTimePtr(ban.LastFailedAt),
		Metadata:     ban.Metadata,
		RevokedAt:    controlTimePtr(ban.RevokedAt),
	}
}

type controlTeamResponse struct {
	ID                uuid.UUID                     `json:"id"`
	Name              string                        `json:"name"`
	Description       string                        `json:"description"`
	Metadata          map[string]any                `json:"metadata"`
	Config            map[string]any                `json:"config"`
	DreamingEffective *dreamservice.EffectiveConfig `json:"dreaming_effective,omitempty"`
	CreatedAt         string                        `json:"created_at"`
	UpdatedAt         string                        `json:"updated_at"`
}

func (h *controlPortalHandler) toControlTeam(ctx context.Context, team *domain.Team) (controlTeamResponse, error) {
	effective, err := effectiveDreamingConfig(ctx, h.appConfig, team.Config)
	if err != nil {
		return controlTeamResponse{}, err
	}
	return controlTeamResponse{
		ID:                team.ID,
		Name:              team.Name,
		Description:       team.Description,
		Metadata:          team.Metadata,
		Config:            team.Config,
		DreamingEffective: effective,
		CreatedAt:         team.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         team.UpdatedAt.Format(time.RFC3339),
	}, nil
}

type controlCredentialResponse struct {
	ID              uuid.UUID `json:"id"`
	TeamID          uuid.UUID `json:"team_id"`
	Name            string    `json:"name"`
	KeySuffix       string    `json:"key_suffix"`
	Scopes          []string  `json:"scopes"`
	Role            string    `json:"role"`
	RateLimit       int       `json:"rate_limit"`
	LastUsedAt      *string   `json:"last_used_at"`
	ExpiresAt       *string   `json:"expires_at"`
	CreatedAt       string    `json:"created_at"`
	MemoryBinding   string    `json:"memory_binding"`
	MemorySpaceKind string    `json:"memory_space_kind"`
}

func toControlCredential(credential *domain.Credential) controlCredentialResponse {
	binding := credential.MemoryBinding
	if !binding.Valid() {
		binding = domain.CredentialBindingSharedOnly
	}
	return controlCredentialResponse{
		ID:              credential.ID,
		TeamID:          credential.GetTeamID(),
		Name:            credential.GetName(),
		KeySuffix:       credential.KeySuffix,
		Scopes:          append([]string{}, credential.Scopes...),
		Role:            credential.GetRole(),
		RateLimit:       credential.RateLimit,
		LastUsedAt:      controlTimePtr(credential.LastUsedAt),
		ExpiresAt:       controlTimePtr(credential.ExpiresAt),
		CreatedAt:       credential.CreatedAt.Format(time.RFC3339),
		MemoryBinding:   string(binding),
		MemorySpaceKind: string(binding.SpaceKind()),
	}
}

func controlTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format(time.RFC3339)
	return &formatted
}

// ShutdownControlPortal gracefully shuts down the control portal server.
func ShutdownControlPortal(e *echo.Echo, logger observability.LogProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil && logger != nil {
		logger.Error("control portal shutdown error", errors.New(tools.SanitizeError(err)))
		return err
	}
	return nil
}
