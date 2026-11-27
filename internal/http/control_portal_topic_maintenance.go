package http

import (
	"errors"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/settings"
	topics "github.com/markhuangai/dense-mem/internal/topics"
)

const topicMaintenanceConfigBodyKey = "topicMaintenanceConfig"
const topicMaintenanceCommandBodyKey = "topicMaintenanceCommand"
const topicMaintenancePauseBodyKey = "topicMaintenancePause"
const topicMaintenanceReassessBodyKey = "topicMaintenanceReassess"

type controlTopicMaintenanceConfigItem struct {
	Key   string `json:"key" validate:"required,max=64"`
	Value string `json:"value" validate:"max=16384"`
}
type controlTopicMaintenanceConfigRequest struct {
	Items []controlTopicMaintenanceConfigItem `json:"items" validate:"required,min=1,max=9,dive"`
}
type controlTopicMaintenanceRunRequest struct {
	OperationKey string `json:"operation_key" validate:"required,max=128"`
	MaxBatches   int    `json:"max_batches" validate:"min=0,max=100"`
}
type controlTopicMaintenancePauseRequest struct {
	OperationKey string `json:"operation_key" validate:"required,max=128"`
}
type controlTopicMaintenanceReassessRequest struct {
	OperationKey string   `json:"operation_key" validate:"required,max=128"`
	EvidenceIDs  []string `json:"evidence_ids" validate:"required,min=1,max=20,dive,uuid"`
}

func registerTopicMaintenanceRoutes(api *echo.Group, h *controlConfigHandler) {
	if h.appConfig != nil {
		api.GET("/config/topic-maintenance", h.getTopicMaintenanceConfig)
		api.PATCH("/config/topic-maintenance", h.updateTopicMaintenanceConfig, middleware.BindAndValidateStrict[controlTopicMaintenanceConfigRequest](topicMaintenanceConfigBodyKey))
	}
	if h.topicMaintenance == nil {
		return
	}
	api.GET("/topic-maintenance/status", h.getTopicMaintenanceStatus)
	api.GET("/topic-maintenance/runs", h.listTopicMaintenanceRuns)
	api.POST("/topic-maintenance/runs", h.runTopicMaintenance, middleware.BindAndValidateStrict[controlTopicMaintenanceRunRequest](topicMaintenanceCommandBodyKey))
	api.POST("/topic-maintenance/runs/:runId/retry", h.retryTopicMaintenance, middleware.BindAndValidateStrict[controlTopicMaintenanceRunRequest](topicMaintenanceCommandBodyKey))
	api.POST("/teams/:teamId/topic-maintenance/reassess", h.reassessTopicMaintenance, middleware.BindAndValidateStrict[controlTopicMaintenanceReassessRequest](topicMaintenanceReassessBodyKey))
	api.POST("/topic-maintenance/pause", h.pauseTopicMaintenance, middleware.BindAndValidateStrict[controlTopicMaintenancePauseRequest](topicMaintenancePauseBodyKey))
	api.POST("/topic-maintenance/resume", h.resumeTopicMaintenance, middleware.BindAndValidateStrict[controlTopicMaintenancePauseRequest](topicMaintenancePauseBodyKey))
}

func (h *controlConfigHandler) getTopicMaintenanceConfig(c *echo.Context) error {
	value, err := h.appConfig.GetTopicMaintenanceSettings(c.Request().Context())
	if err != nil {
		return topicMaintenanceControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlConfigHandler) updateTopicMaintenanceConfig(c *echo.Context) error {
	body := middleware.MustGetValidatedBody[controlTopicMaintenanceConfigRequest](c.Request().Context(), topicMaintenanceConfigBodyKey)
	values := map[string]string{}
	for _, item := range body.Items {
		if _, duplicate := values[item.Key]; duplicate {
			return httperr.New(httperr.VALIDATION_ERROR, "duplicate topic maintenance configuration key")
		}
		values[item.Key] = item.Value
	}
	value, err := h.appConfig.UpdateTopicMaintenanceSettings(c.Request().Context(), values, "control", c.RealIP(), middleware.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return topicMaintenanceControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlConfigHandler) getTopicMaintenanceStatus(c *echo.Context) error {
	value, err := h.topicMaintenance.Status(c.Request().Context())
	if err != nil {
		return topicMaintenanceControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlConfigHandler) listTopicMaintenanceRuns(c *echo.Context) error {
	for key := range c.QueryParams() {
		if key != "limit" && key != "cursor" {
			return httperr.New(httperr.VALIDATION_ERROR, "unknown topic maintenance run query parameter")
		}
	}
	limit := 50
	if raw := c.QueryParam("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid run limit")
		}
	}
	value, err := h.topicMaintenance.Runs(c.Request().Context(), c.QueryParam("cursor"), limit)
	if err != nil {
		return topicMaintenanceControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlConfigHandler) runTopicMaintenance(c *echo.Context) error {
	return h.topicMaintenanceRunCommand(c, "run", "")
}
func (h *controlConfigHandler) retryTopicMaintenance(c *echo.Context) error {
	id, err := parseControlUUID(c.Param("runId"), "run ID")
	if err != nil {
		return err
	}
	return h.topicMaintenanceRunCommand(c, "retry", id.String())
}
func (h *controlConfigHandler) reassessTopicMaintenance(c *echo.Context) error {
	id, err := parseControlUUID(c.Param("teamId"), "team ID")
	if err != nil {
		return err
	}
	body := middleware.MustGetValidatedBody[controlTopicMaintenanceReassessRequest](c.Request().Context(), topicMaintenanceReassessBodyKey)
	return h.topicMaintenanceCommand(c, domain.TopicMaintenanceCommand{OperationKey: body.OperationKey, Action: "reassess", TeamID: id.String(), EvidenceIDs: body.EvidenceIDs})
}
func (h *controlConfigHandler) topicMaintenanceRunCommand(c *echo.Context, action, runID string) error {
	body := middleware.MustGetValidatedBody[controlTopicMaintenanceRunRequest](c.Request().Context(), topicMaintenanceCommandBodyKey)
	return h.topicMaintenanceCommand(c, domain.TopicMaintenanceCommand{OperationKey: body.OperationKey, Action: action, MaxBatches: body.MaxBatches, RetryRunID: runID})
}
func (h *controlConfigHandler) pauseTopicMaintenance(c *echo.Context) error {
	return h.topicMaintenancePauseCommand(c, "pause")
}
func (h *controlConfigHandler) resumeTopicMaintenance(c *echo.Context) error {
	return h.topicMaintenancePauseCommand(c, "resume")
}
func (h *controlConfigHandler) topicMaintenancePauseCommand(c *echo.Context, action string) error {
	body := middleware.MustGetValidatedBody[controlTopicMaintenancePauseRequest](c.Request().Context(), topicMaintenancePauseBodyKey)
	return h.topicMaintenanceCommand(c, domain.TopicMaintenanceCommand{OperationKey: body.OperationKey, Action: action})
}
func (h *controlConfigHandler) topicMaintenanceCommand(c *echo.Context, input domain.TopicMaintenanceCommand) error {
	value, err := h.topicMaintenance.Command(c.Request().Context(), input, c.RealIP(), middleware.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return topicMaintenanceControlError(err)
	}
	return response.SuccessOK(c, value)
}

func topicMaintenanceControlError(err error) error {
	code := topics.MaintenanceErrorCode(err)
	switch {
	case code == "invalid_input", errors.Is(err, settings.ErrInvalidAppConfig):
		return httperr.New(httperr.VALIDATION_ERROR, "invalid topic maintenance input")
	case code == "unauthorized":
		return httperr.New(httperr.FORBIDDEN, "operator authorization required")
	case code == "not_found":
		return httperr.New(httperr.NOT_FOUND, "topic maintenance run not found")
	case code == "conflict":
		return httperr.New(httperr.CONFLICT, "topic maintenance operation conflicts with its durable state")
	case code == "maintenance_paused":
		return httperr.New(httperr.CONFLICT, "topic maintenance is paused")
	case code == "maintenance_disabled":
		return httperr.New(httperr.CONFLICT, "topic maintenance is disabled")
	case code == "window_closed":
		return httperr.New(httperr.CONFLICT, "topic maintenance window is closed")
	case code == "configuration_invalid":
		return httperr.New(httperr.CONFLICT, "configure a topic maintenance context window with sufficient request and response capacity")
	default:
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "topic maintenance unavailable")
	}
}
