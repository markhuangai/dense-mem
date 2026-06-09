package http

import (
	"errors"
	nethttp "net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) getSSOConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	settings, err := h.appConfig.GetSSOSettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSSOConfig(settings)})
}

func (h *controlPortalHandler) updateSSOConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	var body controlSSOConfigRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	values := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		values[item.Key] = item.Value
	}
	settings, err := h.appConfig.UpdateSSOSettings(c.Request().Context(), values, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidAppConfig) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSSOConfig(settings)})
}

type controlSSOConfigRequest struct {
	Items []controlSSOConfigItemRequest `json:"items"`
}

type controlSSOConfigItemRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type controlSSOConfigResponse struct {
	UpdateTime string                         `json:"update_time"`
	Items      []controlSSOConfigItemResponse `json:"items"`
}

type controlSSOConfigItemResponse struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	EffectiveValue string `json:"effective_value"`
	UpdatedAt      string `json:"updated_at"`
}

func toControlSSOConfig(settings *domain.SSOConfigSettings) controlSSOConfigResponse {
	if settings == nil {
		return controlSSOConfigResponse{Items: []controlSSOConfigItemResponse{}}
	}
	items := make([]controlSSOConfigItemResponse, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, controlSSOConfigItemResponse{
			Key:            item.Key,
			Value:          item.Value,
			EffectiveValue: item.EffectiveValue,
			UpdatedAt:      item.UpdatedAt.Format(time.RFC3339),
		})
	}
	return controlSSOConfigResponse{
		UpdateTime: settings.UpdateTime,
		Items:      items,
	}
}
