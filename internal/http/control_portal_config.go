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

func (h *controlPortalHandler) getDreamingConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	settings, err := h.appConfig.GetDreamingSettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlDreamingConfig(settings)})
}

func (h *controlPortalHandler) updateDreamingConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	var body controlDreamingConfigRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	values := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		values[item.Key] = item.Value
	}
	settings, err := h.appConfig.UpdateDreamingSettings(c.Request().Context(), values, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidAppConfig) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlDreamingConfig(settings)})
}

func (h *controlPortalHandler) getOperationLogConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	settings, err := h.appConfig.GetOperationLogSettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlOperationLogConfig(settings)})
}

func (h *controlPortalHandler) updateOperationLogConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	var body controlOperationLogConfigRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	values := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		values[item.Key] = item.Value
	}
	settings, err := h.appConfig.UpdateOperationLogSettings(c.Request().Context(), values, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidAppConfig) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlOperationLogConfig(settings)})
}

type controlSSOConfigRequest struct {
	Items []controlSSOConfigItemRequest `json:"items"`
}

type controlDreamingConfigRequest struct {
	Items []controlSSOConfigItemRequest `json:"items"`
}

type controlOperationLogConfigRequest struct {
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

type controlDreamingConfigResponse struct {
	UpdateTime string                         `json:"update_time"`
	Items      []controlSSOConfigItemResponse `json:"items"`
	Effective  domain.DreamingRuntimeConfig   `json:"effective"`
}

type controlOperationLogConfigResponse struct {
	UpdateTime string                           `json:"update_time"`
	Items      []controlSSOConfigItemResponse   `json:"items"`
	Effective  domain.OperationLogRuntimeConfig `json:"effective"`
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

func toControlDreamingConfig(settings *domain.DreamingConfigSettings) controlDreamingConfigResponse {
	if settings == nil {
		return controlDreamingConfigResponse{Items: []controlSSOConfigItemResponse{}}
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
	return controlDreamingConfigResponse{
		UpdateTime: settings.UpdateTime,
		Items:      items,
		Effective:  settings.Effective,
	}
}

func toControlOperationLogConfig(settings *domain.OperationLogConfigSettings) controlOperationLogConfigResponse {
	if settings == nil {
		return controlOperationLogConfigResponse{Items: []controlSSOConfigItemResponse{}}
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
	return controlOperationLogConfigResponse{
		UpdateTime: settings.UpdateTime,
		Items:      items,
		Effective:  settings.Effective,
	}
}
