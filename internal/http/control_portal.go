package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	httpvalidation "github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	"github.com/markhuangai/dense-mem/internal/tools"
)

// NewControlPortalServer creates the token-protected management portal server.
func NewControlPortalServer(
	cfg httpcontract.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	logger httpcontract.LogProvider,
	securitySvcs ...settings.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetrics(cfg, teamSvc, credentialSvc, nil, HealthConfig{}, logger, securitySvcs...)
}

func NewControlPortalServerWithMetrics(
	cfg httpcontract.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc operations.UsageMetricsReader,
	health HealthConfig,
	logger httpcontract.LogProvider,
	securitySvcs ...settings.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithServices(
		cfg,
		teamSvc,
		credentialSvc,
		metricsSvc,
		ControlPortalServices{},
		health,
		logger,
		securitySvcs...,
	)
}

func NewControlPortalServerWithServices(
	cfg httpcontract.ConfigProvider,
	teamSvc handler.TeamServiceInterface,
	credentialSvc handler.CredentialServiceInterface,
	metricsSvc operations.UsageMetricsReader,
	telemetry ControlPortalServices,
	health HealthConfig,
	logger httpcontract.LogProvider,
	securitySvcs ...settings.SecurityService,
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

	e := echo.NewWithConfig(echo.Config{
		Logger:           NewEchoLogger(logger),
		HTTPErrorHandler: httperr.ErrorHandler,
	})
	applyIPExtractor(e)
	e.Use(observeDelivery)
	e.Use(echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		HandleError:  true,
		LogMethod:    true,
		LogURI:       true,
		LogStatus:    true,
		LogLatency:   true,
		LogRoutePath: true,
		LogValuesFunc: func(c *echo.Context, v echomw.RequestLoggerValues) error {
			if logger == nil {
				return nil
			}
			attrs := transportRequestAttrs(c, v)
			if v.Error != nil {
				httpcontract.LogErrorContext(TransportLogContext(c), logger, "control_http_request", errors.New(tools.SanitizeError(v.Error)), attrs...)
				return nil
			}
			httpcontract.LogInfoContext(TransportLogContext(c), logger, "control_http_request", attrs...)
			return nil
		},
	}))
	e.Use(rootRecover(logger))
	e.Use(httpmw.CorrelationIDMiddleware())
	e.Use(echomw.BodyLimit(int64(controlMaxBodyBytes(cfg))))

	var securitySvc settings.SecurityService
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

	identity := &controlIdentityHandler{sso: telemetry.SSO, directory: telemetry.Directory, controlIdentity: telemetry.ControlIdentity, logger: logger}
	diagnostics := &controlDiagnosticsHandler{auditExport: telemetry.AuditExport, diagnostics: telemetry.Diagnostics,
		metrics: metricsSvc, postgresPool: telemetry.PostgresPool, telemetry: telemetry.Reader, operationLogs: telemetry.Logs,
		recallFeedback: telemetry.RecallFeedback, convergence: telemetry.Convergence, rememberAttempts: telemetry.RememberAttempts,
		rememberInvocations: telemetry.RememberInvocations, health: health, logger: logger}
	settings := &controlConfigHandler{verifierModel: cfg.GetAIVerifierModel(), embeddingModel: cfg.GetAIEmbeddingModel(), appConfig: telemetry.Config, decisions: telemetry.Decisions, topicMaintenance: telemetry.TopicMaintenance, security: securitySvc}
	access := &controlAccessHandler{teams: teamSvc, credentials: credentialSvc, privateMemory: telemetry.PrivateMemory, evidenceConflicts: telemetry.EvidenceConflicts}

	if telemetry.ControlIdentity != nil {
		registerControlIdentityRoutes(e, identity)
	}
	api := e.Group("/control/api")
	api.Use(controlPortalMiddleware(cfg.GetControlPortalToken(), securitySvc, telemetry.ControlIdentity))
	api.Use(httpmw.TelemetryHTTPMiddleware(telemetry.HTTPMetrics))
	if telemetry.AuditExport != nil {
		api.GET("/audit/export", diagnostics.exportAudit)
	}
	if telemetry.Diagnostics != nil {
		api.GET("/diagnostics/bundle", diagnostics.diagnosticBundle)
	}
	api.GET("/session", identity.session)
	api.GET("/metrics", diagnostics.getMetrics)
	api.GET("/telemetry", diagnostics.getTelemetry)
	if telemetry.Convergence != nil {
		api.GET("/search/convergence", diagnostics.getSearchConvergence)
	}
	if telemetry.Logs != nil {
		api.GET("/logs", diagnostics.listOperationLogs)
	}
	if telemetry.RememberAttempts != nil {
		api.GET("/remember-attempts", diagnostics.listRememberAttemptDiagnostics)
		api.GET("/teams/:teamId/remember-attempts/:attemptId", diagnostics.getRememberAttemptDiagnostic)
	}
	registerRememberInvocationDiagnosticsRoutes(api, diagnostics, telemetry)
	if telemetry.Decisions != nil {
		api.GET("/config/decisions", settings.getDecisionConfig)
		api.PATCH("/config/decisions", settings.updateDecisionConfig, httpmw.BindAndValidateStrict[controlDecisionConfigRequest](decisionConfigBodyKey))
	}
	if telemetry.RecallFeedback != nil {
		api.GET("/recall-feedback-events", diagnostics.listRecallFeedbackEvents)
		api.GET("/recall-feedback-events/:recallId", diagnostics.getRecallFeedbackEvent)
	}
	api.GET("/teams", access.listTeams)
	api.POST("/teams", access.createTeam)
	api.PATCH("/teams/:teamId", access.updateTeam)
	api.DELETE("/teams/:teamId", access.deleteTeam)
	if telemetry.EvidenceConflicts != nil {
		api.GET("/teams/:teamId/evidence-conflicts", access.listEvidenceConflicts)
		api.GET("/teams/:teamId/evidence-conflicts/:conflictId", access.getEvidenceConflict)
		api.POST("/teams/:teamId/evidence-conflicts/:conflictId/resolution", access.resolveEvidenceConflict, httpmw.BindAndValidateStrict[controlEvidenceConflictResolutionRequest](evidenceConflictResolutionBodyKey))
	}
	api.GET("/teams/:teamId/credentials", access.listCredentials)
	api.POST("/teams/:teamId/credentials", access.createCredential)
	api.PATCH("/teams/:teamId/credentials/:credentialId", access.updateCredential)
	api.POST("/teams/:teamId/credentials/:credentialId/rotate", access.rotateCredential)
	api.DELETE("/teams/:teamId/credentials/:credentialId", access.deleteCredential)
	api.GET("/security/settings", settings.getSecuritySettings)
	api.PATCH("/security/settings", settings.updateSecuritySettings)
	api.GET("/security/bans", settings.listSecurityBans)
	api.POST("/security/bans", settings.createSecurityBan)
	api.DELETE("/security/bans/:ip", settings.deleteSecurityBan)
	if telemetry.Config != nil {
		api.GET("/config/general", settings.getGeneralConfig)
		api.PATCH("/config/general", settings.updateGeneralConfig)
		api.GET("/config/sso", settings.getSSOConfig)
		api.PATCH("/config/sso", settings.updateSSOConfig)
		api.GET("/config/operation-logs", settings.getOperationLogConfig)
		api.PATCH("/config/operation-logs", settings.updateOperationLogConfig)
		api.GET("/config/recall-feedback", settings.getRecallFeedbackConfig)
		api.PATCH("/config/recall-feedback", settings.updateRecallFeedbackConfig)
		api.GET("/config/telemetry-pricing", settings.getTelemetryPricingConfig)
		api.PATCH("/config/telemetry-pricing", settings.updateTelemetryPricingConfig)
		api.GET("/config/private-memory", settings.getPrivateMemoryConfig)
		api.PATCH("/config/private-memory", settings.updatePrivateMemoryConfig, httpmw.BindAndValidateStrict[controlPrivateMemoryConfigRequest](privateMemoryConfigBodyKey))
	}
	registerTopicMaintenanceRoutes(api, settings)
	if telemetry.PrivateMemory != nil {
		api.GET("/private-memory/spaces", access.listPrivateMemorySpaces)
		api.POST("/private-memory/spaces/:spaceId/legal-hold", access.placePrivateMemoryLegalHold, httpmw.BindAndValidateStrict[dto.PrivateMemoryLegalHoldRequest](privateMemoryLegalHoldBodyKey))
		api.DELETE("/private-memory/spaces/:spaceId/legal-hold", access.releasePrivateMemoryLegalHold)
		api.POST("/private-memory/spaces/:spaceId/erasures", access.requestControlPrivateMemoryErasure, httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		api.GET("/private-memory/erasures", access.listPrivateMemoryErasures)
		api.GET("/private-memory/erasures/:operationId", access.getPrivateMemoryErasure)
		api.POST("/private-memory/retention-runs", access.runPrivateMemoryRetention, httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))
		api.GET("/private-memory/retention-runs", access.listPrivateMemoryRetentionRuns)
	}
	if telemetry.SSO != nil {
		api.GET("/sso/providers", identity.listSSOProviders)
		api.POST("/sso/providers", identity.createSSOProvider)
		api.PATCH("/sso/providers/:providerId", identity.updateSSOProvider)
		api.DELETE("/sso/providers/:providerId", identity.deleteSSOProvider)
		api.GET("/sso/providers/:providerId/mappings", identity.listSSOMappings)
		api.POST("/sso/providers/:providerId/mappings", identity.createSSOMapping)
		api.PATCH("/sso/providers/:providerId/mappings/:mappingId", identity.updateSSOMapping)
		api.DELETE("/sso/providers/:providerId/mappings/:mappingId", identity.deleteSSOMapping)
	}
	if telemetry.Directory != nil {
		api.GET("/sso/directory/connectors", identity.listDirectoryConnectors)
		api.GET("/sso/providers/:providerId/directory-connector", identity.getDirectoryConnector)
		api.POST("/sso/providers/:providerId/directory-connector", identity.createDirectoryConnector)
		api.PATCH("/sso/directory/connectors/:connectorId", identity.updateDirectoryConnector)
		api.POST("/sso/directory/connectors/:connectorId/credentials/rotate", identity.rotateDirectoryCredentials)
		api.GET("/sso/directory/connectors/:connectorId/preview", identity.previewDirectoryConnector)
		api.POST("/sso/directory/connectors/:connectorId/status", identity.setDirectoryConnectorStatus)
		api.POST("/sso/directory/connectors/:connectorId/groups/:groupId/adopt", identity.adoptDirectoryGroupTeam)
	}
	if telemetry.ControlIdentity != nil {
		api.GET("/sso/providers/:providerId/control-admin-groups", identity.listControlAdminGroups)
		api.POST("/sso/providers/:providerId/control-admin-groups", identity.createControlAdminGroup)
		api.PATCH("/sso/providers/:providerId/control-admin-groups/:groupId", identity.updateControlAdminGroup)
		api.DELETE("/sso/providers/:providerId/control-admin-groups/:groupId", identity.deleteControlAdminGroup)
	}

	if staticDir := defaultPortalStaticDir(); staticDir != "" {
		e.Static("/", staticDir)
	}

	return e, nil
}

func (h *controlDiagnosticsHandler) getMetrics(c *echo.Context) error {
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
	var pool *PostgresPoolMetrics
	if h.postgresPool != nil {
		value := h.postgresPool()
		pool = &value
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": controlMetricsResponse{
		Window:                  snapshot.Window,
		System:                  snapshot.System,
		DependenciesCheckedAt:   dependencyReport.CheckedAt,
		Dependencies:            dependencyReport.Dependencies,
		Teams:                   snapshot.Teams,
		Keys:                    snapshot.Keys,
		Routes:                  snapshot.Routes,
		ApplicationPostgresPool: pool,
	}})
}

func (h *controlDiagnosticsHandler) getTelemetry(c *echo.Context) error {
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

func (h *controlAccessHandler) listTeams(c *echo.Context) error {
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
	return response.PaginatedOK(c, items, response.Pagination{Limit: limit, Offset: offset, Total: total})
}

func (h *controlAccessHandler) createTeam(c *echo.Context) error {
	var body dto.CreateTeamRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if err := httpvalidation.ValidateStruct(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}
	team, err := h.teams.Create(c.Request().Context(), accessservice.CreateTeamRequest{
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

func (h *controlAccessHandler) updateTeam(c *echo.Context) error {
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
	team, err := h.teams.Update(c.Request().Context(), teamID, accessservice.UpdateTeamRequest{
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

func (h *controlAccessHandler) deleteTeam(c *echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	if err := h.teams.Delete(c.Request().Context(), teamID, nil, "control", c.RealIP(), ""); err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

func (h *controlAccessHandler) listCredentials(c *echo.Context) error {
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
	return response.PaginatedOK(c, items, response.Pagination{Limit: limit, Offset: offset, Total: total})
}

func (h *controlAccessHandler) createCredential(c *echo.Context) error {
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	var body controlCreateCredentialRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	req := accessservice.CreateCredentialRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
		Role:      body.Role,
	}
	if err := dto.RejectMemoryBinding(body.MemoryBinding); err != nil {
		return err
	}
	if body.Scopes != nil {
		if len(*body.Scopes) == 0 {
			return httperr.New(httperr.VALIDATION_ERROR, accessservice.CredentialScopeValidationMessage())
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

func (h *controlAccessHandler) updateCredential(c *echo.Context) error {
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
		if _, err := accessservice.NormalizeCredentialRole(body.Role); err != nil {
			return err
		}
	}
	if scopesPresent && len(*body.Scopes) == 0 {
		return httperr.New(httperr.VALIDATION_ERROR, accessservice.CredentialScopeValidationMessage())
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

func (h *controlAccessHandler) rotateCredential(c *echo.Context) error {
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
	req := accessservice.CreateCredentialRequest{
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

func (h *controlAccessHandler) deleteCredential(c *echo.Context) error {
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

func (h *controlConfigHandler) getSecuritySettings(c *echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	settings, err := h.security.GetSecuritySettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSecuritySettings(settings)})
}

func (h *controlConfigHandler) updateSecuritySettings(c *echo.Context) error {
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
		if errors.Is(err, settings.ErrInvalidSecuritySettings) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSecuritySettings(updated)})
}

func (h *controlConfigHandler) listSecurityBans(c *echo.Context) error {
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
	return response.PaginatedOK(c, items, response.Pagination{Limit: limit, Offset: offset, Total: total})
}

func (h *controlConfigHandler) createSecurityBan(c *echo.Context) error {
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
		if errors.Is(err, settings.ErrInvalidSecurityIP) || errors.Is(err, settings.ErrInvalidSecuritySettings) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": toControlSecurityBan(*ban)})
}

func (h *controlConfigHandler) deleteSecurityBan(c *echo.Context) error {
	if h.security == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "security service unavailable")
	}
	ip := c.Param("ip")
	if err := h.security.DeleteSecurityBan(c.Request().Context(), ip, "control", c.RealIP(), ""); err != nil {
		if errors.Is(err, settings.ErrInvalidSecurityIP) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

type controlCreateCredentialRequest struct {
	Name          string          `json:"name"`
	Scopes        *[]string       `json:"scopes"`
	Role          string          `json:"role"`
	RateLimit     int             `json:"rate_limit"`
	ExpiresAt     *string         `json:"expires_at"`
	MemoryBinding json.RawMessage `json:"memory_binding"`
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
	Window                  domain.UsageMetricsWindow   `json:"window"`
	System                  domain.UsageMetricTotal     `json:"system"`
	DependenciesCheckedAt   string                      `json:"dependencies_checked_at"`
	Dependencies            []controlDependencyResponse `json:"dependencies"`
	Teams                   []domain.UsageTeamMetric    `json:"teams"`
	Keys                    []domain.UsageKeyMetric     `json:"keys"`
	Routes                  []domain.UsageRouteMetric   `json:"routes"`
	ApplicationPostgresPool *PostgresPoolMetrics        `json:"application_postgres_pool,omitempty"`
}

type controlDependencyResponse struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	LatencyMS  *int64  `json:"latency_ms"`
	Message    *string `json:"message,omitempty"`
	ReasonCode *string `json:"reason_code,omitempty"`
}

func controlTelemetryFilter(c *echo.Context) (operations.TelemetryFilter, error) {
	scope := strings.TrimSpace(c.QueryParam("scope"))
	if scope == "" {
		scope = "system"
	}
	switch scope {
	case "system", "team", "profile":
	default:
		return operations.TelemetryFilter{}, httperr.New(httperr.VALIDATION_ERROR, "scope must be one of system, team, profile")
	}

	var teamID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return operations.TelemetryFilter{}, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
		}
		teamID = &parsed
	}

	var profileID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("profile_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return operations.TelemetryFilter{}, httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
		}
		profileID = &parsed
	}

	return operations.TelemetryFilter{
		Window:    strings.TrimSpace(c.QueryParam("window")),
		Scope:     scope,
		TeamID:    teamID,
		ProfileID: profileID,
		Audience:  operations.TelemetryAudienceOperator,
	}, nil
}

type controlBodyLimitConfig interface {
	GetHTTPMaxBodyBytes() int
}

func controlMaxBodyBytes(cfg httpcontract.BodyLimitConfig) int {
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
func controlTeamIDParam(c *echo.Context) string {
	return c.Param("teamId")
}

func controlCredentialIDParam(c *echo.Context) string {
	return c.Param("credentialId")
}

func controlPagination(c *echo.Context) (int, int) {
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
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	Config      map[string]any `json:"config"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (h *controlAccessHandler) toControlTeam(ctx context.Context, team *domain.Team) (controlTeamResponse, error) {
	return controlTeamResponse{
		ID:          team.ID,
		Name:        team.Name,
		Description: team.Description,
		Metadata:    team.Metadata,
		Config:      team.Config,
		CreatedAt:   team.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   team.UpdatedAt.Format(time.RFC3339),
	}, nil
}

type controlCredentialResponse struct {
	ID           uuid.UUID           `json:"id"`
	TeamID       uuid.UUID           `json:"team_id"`
	Name         string              `json:"name"`
	KeySuffix    string              `json:"key_suffix"`
	Scopes       []string            `json:"scopes"`
	Role         string              `json:"role"`
	RateLimit    int                 `json:"rate_limit"`
	LastUsedAt   *string             `json:"last_used_at"`
	ExpiresAt    *string             `json:"expires_at"`
	CreatedAt    string              `json:"created_at"`
	MemoryAccess domain.MemoryAccess `json:"memory_access"`
}

func toControlCredential(credential *domain.Credential) controlCredentialResponse {
	return controlCredentialResponse{
		ID:           credential.ID,
		TeamID:       credential.GetTeamID(),
		Name:         credential.GetName(),
		KeySuffix:    credential.KeySuffix,
		Scopes:       append([]string{}, credential.Scopes...),
		Role:         credential.GetRole(),
		RateLimit:    credential.RateLimit,
		LastUsedAt:   controlTimePtr(credential.LastUsedAt),
		ExpiresAt:    controlTimePtr(credential.ExpiresAt),
		CreatedAt:    credential.CreatedAt.Format(time.RFC3339),
		MemoryAccess: credential.MemoryCapabilities(),
	}
}
func controlTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format(time.RFC3339)
	return &formatted
}
func ShutdownControlPortal(server *nethttp.Server, logger httpcontract.LogProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && logger != nil {
		logger.Error("control portal shutdown error", errors.New(tools.SanitizeError(err)))
		return err
	}
	return nil
}
