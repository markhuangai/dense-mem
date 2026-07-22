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
)

// NewControlPortalServer creates the token-protected management portal server.
func NewControlPortalServer(
	cfg config.ConfigProvider,
	profileSvc handler.ProfileServiceInterface,
	apiKeySvc handler.APIKeyServiceInterface,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetrics(cfg, profileSvc, apiKeySvc, nil, HealthConfig{}, logger, securitySvcs...)
}

func NewControlPortalServerWithMetrics(
	cfg config.ConfigProvider,
	profileSvc handler.ProfileServiceInterface,
	apiKeySvc handler.APIKeyServiceInterface,
	metricsSvc service.UsageMetricsReader,
	health HealthConfig,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	return NewControlPortalServerWithMetricsAndTelemetry(
		cfg,
		profileSvc,
		apiKeySvc,
		metricsSvc,
		ControlPortalTelemetry{},
		health,
		logger,
		securitySvcs...,
	)
}

func NewControlPortalServerWithMetricsAndTelemetry(
	cfg config.ConfigProvider,
	profileSvc handler.ProfileServiceInterface,
	apiKeySvc handler.APIKeyServiceInterface,
	metricsSvc service.UsageMetricsReader,
	telemetry ControlPortalTelemetry,
	health HealthConfig,
	logger observability.LogProvider,
	securitySvcs ...service.SecurityService,
) (*echo.Echo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("control portal: config is required")
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
				logger.Error("control_http_request", v.Error, attrs...)
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
		e.GET("/metrics", echo.WrapHandler(telemetry.ScrapeHandler), telemetryScrapeTokenMiddleware(telemetry.ScrapeToken))
	}

	control := &controlPortalHandler{profiles: profileSvc, keys: apiKeySvc, security: securitySvc, metrics: metricsSvc, telemetry: telemetry.Reader, operationLogs: telemetry.Logs, recallFeedback: telemetry.RecallFeedback, dreams: telemetry.Dreams, migration: telemetry.Migration, health: health, sso: telemetry.SSO, appConfig: telemetry.Config}
	api := e.Group("/control/api")
	api.Use(controlPortalMiddleware(cfg.GetControlPortalToken(), securitySvc))
	api.Use(httpmw.TelemetryHTTPMiddleware(telemetry.HTTPMetrics))
	api.GET("/session", control.session)
	api.GET("/metrics", control.getMetrics)
	api.GET("/telemetry", control.getTelemetry)
	registerV2MigrationControlRoutes(api, control)
	if telemetry.Logs != nil {
		api.GET("/logs", control.listOperationLogs)
	}
	if telemetry.RecallFeedback != nil {
		api.GET("/recall-feedback-events", control.listRecallFeedbackEvents)
		api.GET("/recall-feedback-events/:recallId", control.getRecallFeedbackEvent)
	}
	api.GET("/teams", control.listProfiles)
	api.POST("/teams", control.createProfile)
	api.PATCH("/teams/:teamId", control.updateProfile)
	api.DELETE("/teams/:teamId", control.deleteProfile)
	if telemetry.Dreams != nil {
		api.GET("/teams/:teamId/dreaming/status", control.getTeamDreamingStatus)
		api.GET("/teams/:teamId/dreaming/runs", control.listTeamDreamingRuns)
		api.GET("/teams/:teamId/dreams", control.listTeamDreams)
		api.GET("/teams/:teamId/dreams/:dreamId", control.getTeamDream)
	}
	api.GET("/teams/:teamId/profiles", control.listAPIKeys)
	api.POST("/teams/:teamId/profiles", control.createAPIKey)
	api.PATCH("/teams/:teamId/profiles/:profileId", control.updateAPIKey)
	api.POST("/teams/:teamId/profiles/:profileId/rotate", control.rotateAPIKey)
	api.DELETE("/teams/:teamId/profiles/:profileId", control.deleteAPIKey)
	// Legacy aliases retained while the portal and operator tooling move to teams.
	api.GET("/profiles", control.listProfiles)
	api.POST("/profiles", control.createProfile)
	api.PATCH("/profiles/:profileId", control.updateProfile)
	api.DELETE("/profiles/:profileId", control.deleteProfile)
	api.GET("/profiles/:profileId/api-keys", control.listAPIKeys)
	api.POST("/profiles/:profileId/api-keys", control.createAPIKey)
	api.PATCH("/profiles/:profileId/api-keys/:keyId", control.updateAPIKey)
	api.POST("/profiles/:profileId/api-keys/:keyId/rotate", control.rotateAPIKey)
	api.DELETE("/profiles/:profileId/api-keys/:keyId", control.deleteAPIKey)
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
		api.GET("/config/evaluation", control.getEvaluationConfig)
		api.PATCH("/config/evaluation", control.updateEvaluationConfig)
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
	return c.JSON(nethttp.StatusOK, map[string]any{"data": controlMetricsResponse{
		Window:       snapshot.Window,
		System:       snapshot.System,
		Dependencies: controlDependencySnapshot(c.Request().Context(), h.health),
		Teams:        snapshot.Teams,
		Keys:         snapshot.Keys,
		Routes:       snapshot.Routes,
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

func (h *controlPortalHandler) listProfiles(c echo.Context) error {
	limit, offset := controlPagination(c)
	profiles, err := h.profiles.List(c.Request().Context(), limit, offset)
	if err != nil {
		return err
	}
	total, err := h.profiles.Count(c.Request().Context())
	if err != nil {
		return err
	}
	items := make([]controlProfileResponse, 0, len(profiles))
	for _, profile := range profiles {
		item, err := h.toControlProfile(c.Request().Context(), profile)
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

func (h *controlPortalHandler) createProfile(c echo.Context) error {
	var body dto.CreateProfileRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if err := httpvalidation.ValidateStruct(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}
	profile, err := h.profiles.Create(c.Request().Context(), service.CreateProfileRequest{
		Name:        body.Name,
		Description: body.Description,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	item, err := h.toControlProfile(c.Request().Context(), profile)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": item})
}

func (h *controlPortalHandler) updateProfile(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	var body dto.UpdateProfileRequest
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
	profile, err := h.profiles.Update(c.Request().Context(), profileID, service.UpdateProfileRequest{
		Name:        namePtr,
		Description: descPtr,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	item, err := h.toControlProfile(c.Request().Context(), profile)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": item})
}

func (h *controlPortalHandler) deleteProfile(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	if err := h.profiles.Delete(c.Request().Context(), profileID, nil, "control", c.RealIP(), ""); err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "deleted"}})
}

func (h *controlPortalHandler) listAPIKeys(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	limit, offset := controlPagination(c)
	keys, err := h.keys.ListByProfile(c.Request().Context(), profileID, limit, offset)
	if err != nil {
		return err
	}
	total, err := h.keys.CountByProfile(c.Request().Context(), profileID)
	if err != nil {
		return err
	}
	items := make([]controlAPIKeyResponse, 0, len(keys))
	for _, key := range keys {
		items = append(items, toControlAPIKey(key))
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data:       items,
		Pagination: handler.Pagination{Limit: limit, Offset: offset, Total: total},
	})
}

func (h *controlPortalHandler) createAPIKey(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	var body controlCreateAPIKeyRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	req := service.CreateAPIKeyRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
		Role:      body.Role,
	}
	if body.Scopes != nil {
		if len(*body.Scopes) == 0 {
			return httperr.New(httperr.VALIDATION_ERROR, service.APIKeyScopeValidationMessage())
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
	key, rawKey, err := h.keys.CreateStandardKey(c.Request().Context(), profileID, req, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{
		"data": map[string]any{
			"api_key": rawKey,
			"key":     toControlAPIKey(key),
		},
	})
}

func (h *controlPortalHandler) updateAPIKey(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	keyID, err := parseControlUUID(controlTeamProfileIDParam(c), "profile ID")
	if err != nil {
		return err
	}
	var body controlUpdateAPIKeyRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	namePresent := strings.TrimSpace(body.Name) != ""
	rolePresent := strings.TrimSpace(body.Role) != ""
	scopesPresent := body.Scopes != nil
	if !namePresent && !rolePresent && !scopesPresent {
		return httperr.New(httperr.VALIDATION_ERROR, "profile name, role, or scopes is required")
	}
	if boolCount(namePresent, rolePresent, scopesPresent) > 1 {
		return httperr.New(httperr.VALIDATION_ERROR, "profile name, role, and scopes must be updated separately")
	}
	if rolePresent {
		if _, err := service.NormalizeAPIKeyRole(body.Role); err != nil {
			return err
		}
	}
	if scopesPresent && len(*body.Scopes) == 0 {
		return httperr.New(httperr.VALIDATION_ERROR, service.APIKeyScopeValidationMessage())
	}
	var key *domain.APIKey
	if namePresent {
		key, err = h.keys.UpdateNameForProfile(c.Request().Context(), profileID, keyID, body.Name, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	if rolePresent {
		key, err = h.keys.UpdateRoleForProfile(c.Request().Context(), profileID, keyID, body.Role, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	if scopesPresent {
		key, err = h.keys.UpdateScopesForProfile(c.Request().Context(), profileID, keyID, *body.Scopes, nil, "control", c.RealIP(), "")
		if err != nil {
			return err
		}
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlAPIKey(key)})
}

func (h *controlPortalHandler) rotateAPIKey(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	keyID, err := parseControlUUID(controlTeamProfileIDParam(c), "profile ID")
	if err != nil {
		return err
	}
	var body controlCreateAPIKeyRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if body.Scopes != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "scopes cannot be changed by rotating a key")
	}
	if strings.TrimSpace(body.Role) != "" {
		return httperr.New(httperr.VALIDATION_ERROR, "role cannot be changed by rotating a key")
	}
	req := service.CreateAPIKeyRequest{
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
	key, rawKey, err := h.keys.RotateForProfile(c.Request().Context(), profileID, keyID, req, nil, "control", c.RealIP(), "")
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{
		"data": map[string]any{
			"api_key": rawKey,
			"key":     toControlAPIKey(key),
		},
	})
}

func (h *controlPortalHandler) deleteAPIKey(c echo.Context) error {
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	keyID, err := parseControlUUID(controlTeamProfileIDParam(c), "profile ID")
	if err != nil {
		return err
	}
	if err := h.keys.DeleteForProfile(c.Request().Context(), profileID, keyID, nil, "control", c.RealIP(), ""); err != nil {
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

type controlCreateAPIKeyRequest struct {
	Name      string    `json:"name"`
	Scopes    *[]string `json:"scopes"`
	Role      string    `json:"role"`
	RateLimit int       `json:"rate_limit"`
	ExpiresAt *string   `json:"expires_at"`
}

type controlUpdateAPIKeyRequest struct {
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
	Window       domain.UsageMetricsWindow   `json:"window"`
	System       domain.UsageMetricTotal     `json:"system"`
	Dependencies []controlDependencyResponse `json:"dependencies"`
	Teams        []domain.UsageTeamMetric    `json:"teams"`
	Keys         []domain.UsageKeyMetric     `json:"keys"`
	Routes       []domain.UsageRouteMetric   `json:"routes"`
}

type controlDependencyResponse struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	LatencyMS *int64  `json:"latency_ms"`
	Message   *string `json:"message,omitempty"`
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
	}, nil
}

func telemetryScrapeTokenMiddleware(token string) echo.MiddlewareFunc {
	expected := strings.TrimSpace(token)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			got := c.Request().Header.Get("X-Telemetry-Scrape-Token")
			if got == "" {
				auth := c.Request().Header.Get(echo.HeaderAuthorization)
				if strings.HasPrefix(auth, "Bearer ") {
					got = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
				return httperr.New(httperr.AUTH_INVALID, "invalid telemetry scrape token")
			}
			return next(c)
		}
	}
}

func controlPortalMiddleware(token string, securitySvc service.SecurityService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			origin := c.Request().Header.Get(echo.HeaderOrigin)
			if origin != "" {
				c.Response().Header().Set(echo.HeaderVary, echo.HeaderOrigin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowOrigin, origin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowHeaders, "Authorization, Content-Type, X-Control-Portal-Token")
				c.Response().Header().Set(echo.HeaderAccessControlAllowMethods, "GET, POST, PATCH, DELETE, OPTIONS")
			}
			if c.Request().Method == nethttp.MethodOptions {
				return c.NoContent(nethttp.StatusNoContent)
			}
			if !controlTokenMatches(c.Request(), token) {
				recordControlAuthFailure(c, securitySvc)
				return httperr.New(httperr.AUTH_INVALID, "invalid control portal token")
			}
			ctx := context.WithValue(c.Request().Context(), controlPortalActorContextKey{}, controlPortalActorFromRequest(c.Request()))
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func recordControlAuthFailure(c echo.Context, securitySvc service.SecurityService) {
	if securitySvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := securitySvc.RecordAuthFailure(ctx, c.RealIP(), "control", "AUTH_INVALID"); err != nil {
		c.Logger().Errorf("control security auth failure record failed: %v", err)
	}
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
	if v := c.Param("teamId"); v != "" {
		return v
	}
	return c.Param("profileId")
}

func controlTeamProfileIDParam(c echo.Context) string {
	if v := c.Param("keyId"); v != "" {
		return v
	}
	return c.Param("profileId")
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

type controlProfileResponse struct {
	ID                uuid.UUID                     `json:"id"`
	Name              string                        `json:"name"`
	Description       string                        `json:"description"`
	Metadata          map[string]any                `json:"metadata"`
	Config            map[string]any                `json:"config"`
	DreamingEffective *dreamservice.EffectiveConfig `json:"dreaming_effective,omitempty"`
	CreatedAt         string                        `json:"created_at"`
	UpdatedAt         string                        `json:"updated_at"`
}

func (h *controlPortalHandler) toControlProfile(ctx context.Context, profile *domain.Profile) (controlProfileResponse, error) {
	effective, err := effectiveDreamingConfig(ctx, h.appConfig, profile.Config)
	if err != nil {
		return controlProfileResponse{}, err
	}
	return controlProfileResponse{
		ID:                profile.ID,
		Name:              profile.Name,
		Description:       profile.Description,
		Metadata:          profile.Metadata,
		Config:            profile.Config,
		DreamingEffective: effective,
		CreatedAt:         profile.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         profile.UpdatedAt.Format(time.RFC3339),
	}, nil
}

type controlAPIKeyResponse struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	Scopes     []string  `json:"scopes"`
	Role       string    `json:"role"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

func toControlAPIKey(key *domain.APIKey) controlAPIKeyResponse {
	return controlAPIKeyResponse{
		ID:         key.ID,
		TeamID:     key.GetTeamID(),
		Name:       key.GetProfileName(),
		KeySuffix:  key.KeySuffix,
		Scopes:     append([]string{}, key.Scopes...),
		Role:       key.GetRole(),
		RateLimit:  key.RateLimit,
		LastUsedAt: controlTimePtr(key.LastUsedAt),
		ExpiresAt:  controlTimePtr(key.ExpiresAt),
		CreatedAt:  key.CreatedAt.Format(time.RFC3339),
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
		logger.Error("control portal shutdown error", err)
		return err
	}
	return nil
}
