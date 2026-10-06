package http

import (
	"errors"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	ontology "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/settings"
)

const ontologyConfigBodyKey = "ontologyConfig"
const ontologyCommandBodyKey = "ontologyCommand"
const ontologyPauseBodyKey = "ontologyPause"

type controlOntologyConfigItem struct {
	Key   string `json:"key" validate:"required,max=64"`
	Value string `json:"value" validate:"max=256"`
}
type controlOntologyConfigRequest struct {
	Items []controlOntologyConfigItem `json:"items" validate:"required,min=1,max=7,dive"`
}
type controlOntologyRunRequest struct {
	OperationKey string `json:"operation_key" validate:"required,max=128"`
	MaxBatches   int    `json:"max_batches" validate:"min=0,max=100"`
}
type controlOntologyPauseRequest struct {
	OperationKey string `json:"operation_key" validate:"required,max=128"`
}

func registerOntologyMaintenanceRoutes(api *echo.Group, h *controlPortalHandler) {
	if h.appConfig != nil {
		api.GET("/config/ontology-maintenance", h.getOntologyMaintenanceConfig)
		api.PATCH("/config/ontology-maintenance", h.updateOntologyMaintenanceConfig, middleware.BindAndValidateStrict[controlOntologyConfigRequest](ontologyConfigBodyKey))
	}
	if h.ontology == nil {
		return
	}
	api.GET("/ontology/status", h.getOntologyMaintenanceStatus)
	api.GET("/ontology/runs", h.listOntologyMaintenanceRuns)
	api.POST("/ontology/runs", h.runOntologyMaintenance, middleware.BindAndValidateStrict[controlOntologyRunRequest](ontologyCommandBodyKey))
	api.POST("/ontology/runs/:runId/retry", h.retryOntologyMaintenance, middleware.BindAndValidateStrict[controlOntologyRunRequest](ontologyCommandBodyKey))
	api.POST("/ontology/pause", h.pauseOntologyMaintenance, middleware.BindAndValidateStrict[controlOntologyPauseRequest](ontologyPauseBodyKey))
	api.POST("/ontology/resume", h.resumeOntologyMaintenance, middleware.BindAndValidateStrict[controlOntologyPauseRequest](ontologyPauseBodyKey))
}

func (h *controlPortalHandler) getOntologyMaintenanceConfig(c echo.Context) error {
	value, err := h.appConfig.GetOntologyMaintenanceSettings(c.Request().Context())
	if err != nil {
		return ontologyControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlPortalHandler) updateOntologyMaintenanceConfig(c echo.Context) error {
	body := middleware.MustGetValidatedBody[controlOntologyConfigRequest](c.Request().Context(), ontologyConfigBodyKey)
	values := map[string]string{}
	for _, item := range body.Items {
		if _, duplicate := values[item.Key]; duplicate {
			return httperr.New(httperr.VALIDATION_ERROR, "duplicate ontology configuration key")
		}
		values[item.Key] = item.Value
	}
	value, err := h.appConfig.UpdateOntologyMaintenanceSettings(c.Request().Context(), values, "control", c.RealIP(), middleware.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return ontologyControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlPortalHandler) getOntologyMaintenanceStatus(c echo.Context) error {
	value, err := h.ontology.Status(c.Request().Context())
	if err != nil {
		return ontologyControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlPortalHandler) listOntologyMaintenanceRuns(c echo.Context) error {
	for key := range c.QueryParams() {
		if key != "limit" && key != "cursor" {
			return httperr.New(httperr.VALIDATION_ERROR, "unknown ontology run query parameter")
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
	value, err := h.ontology.Runs(c.Request().Context(), c.QueryParam("cursor"), limit)
	if err != nil {
		return ontologyControlError(err)
	}
	return response.SuccessOK(c, value)
}

func (h *controlPortalHandler) runOntologyMaintenance(c echo.Context) error {
	return h.ontologyRunCommand(c, "run", "")
}
func (h *controlPortalHandler) retryOntologyMaintenance(c echo.Context) error {
	id, err := parseControlUUID(c.Param("runId"), "run ID")
	if err != nil {
		return err
	}
	return h.ontologyRunCommand(c, "retry", id.String())
}
func (h *controlPortalHandler) ontologyRunCommand(c echo.Context, action, runID string) error {
	body := middleware.MustGetValidatedBody[controlOntologyRunRequest](c.Request().Context(), ontologyCommandBodyKey)
	return h.ontologyCommand(c, domain.OntologyMaintenanceCommand{OperationKey: body.OperationKey, Action: action, MaxBatches: body.MaxBatches, RetryRunID: runID})
}
func (h *controlPortalHandler) pauseOntologyMaintenance(c echo.Context) error {
	return h.ontologyPauseCommand(c, "pause")
}
func (h *controlPortalHandler) resumeOntologyMaintenance(c echo.Context) error {
	return h.ontologyPauseCommand(c, "resume")
}
func (h *controlPortalHandler) ontologyPauseCommand(c echo.Context, action string) error {
	body := middleware.MustGetValidatedBody[controlOntologyPauseRequest](c.Request().Context(), ontologyPauseBodyKey)
	return h.ontologyCommand(c, domain.OntologyMaintenanceCommand{OperationKey: body.OperationKey, Action: action})
}
func (h *controlPortalHandler) ontologyCommand(c echo.Context, input domain.OntologyMaintenanceCommand) error {
	value, err := h.ontology.Command(c.Request().Context(), input, c.RealIP(), middleware.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return ontologyControlError(err)
	}
	return response.SuccessOK(c, value)
}

func ontologyControlError(err error) error {
	code := ontology.MaintenanceErrorCode(err)
	switch {
	case code == "invalid_input", errors.Is(err, settings.ErrInvalidAppConfig):
		return httperr.New(httperr.VALIDATION_ERROR, "invalid ontology maintenance input")
	case code == "unauthorized":
		return httperr.New(httperr.FORBIDDEN, "operator authorization required")
	case code == "not_found":
		return httperr.New(httperr.NOT_FOUND, "ontology run not found")
	case code == "conflict":
		return httperr.New(httperr.CONFLICT, "ontology operation conflicts with its durable state")
	case code == "maintenance_paused":
		return httperr.New(httperr.CONFLICT, "ontology maintenance is paused")
	case code == "maintenance_disabled":
		return httperr.New(httperr.CONFLICT, "ontology maintenance is disabled")
	case code == "budget_deferred":
		return httperr.New(httperr.CONFLICT, "ontology work is deferred by its window budget")
	default:
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "ontology maintenance unavailable")
	}
}
