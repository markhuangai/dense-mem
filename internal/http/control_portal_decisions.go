package http

import (
	"errors"

	"github.com/labstack/echo/v4"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/settings"
)

const decisionConfigBodyKey = "decision_config_body"

type controlDecisionConfigRequest struct {
	Items []controlTelemetryPricingConfigItemRequest `json:"items" validate:"required,min=1,max=4,dive"`
}

func (h *controlPortalHandler) getDecisionConfig(c echo.Context) error {
	value, err := h.decisions.GetDecisionSettings(c.Request().Context())
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "decision configuration unavailable")
	}
	return response.SuccessOK(c, value)
}

func (h *controlPortalHandler) updateDecisionConfig(c echo.Context) error {
	body := httpmw.MustGetValidatedBody[controlDecisionConfigRequest](c.Request().Context(), decisionConfigBodyKey)
	values := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		if _, duplicate := values[item.Key]; duplicate {
			return httperr.New(httperr.VALIDATION_ERROR, "duplicate decision configuration key")
		}
		if item.Value == nil {
			return httperr.New(httperr.VALIDATION_ERROR, "decision preference value is required")
		}
		values[item.Key] = *item.Value
	}
	value, err := h.decisions.UpdateDecisionSettings(c.Request().Context(), values, "control", c.RealIP(), httpmw.GetCorrelationID(c.Request().Context()))
	if err != nil {
		if errors.Is(err, settings.ErrInvalidAppConfig) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "decision configuration update failed")
	}
	return response.SuccessOK(c, value)
}
