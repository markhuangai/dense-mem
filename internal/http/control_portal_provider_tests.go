package http

import (
	"errors"

	"github.com/labstack/echo/v4"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/operations"
)

const providerTestBodyKey = "provider_test_body"

type controlProviderTestRequest struct {
	TargetID string `json:"target_id" validate:"required,len=64,hexadecimal"`
}

func (h *controlPortalHandler) listProviderTests(c echo.Context) error {
	targets, err := h.providerTests.List(c.Request().Context())
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "provider configuration unavailable")
	}
	return response.SuccessOK(c, targets)
}

func (h *controlPortalHandler) runProviderTest(c echo.Context) error {
	body := httpmw.MustGetValidatedBody[controlProviderTestRequest](c.Request().Context(), providerTestBodyKey)
	actor := controlPortalActorFromContext(c.Request().Context())
	if identity := controlPortalActorIdentityFromContext(c.Request().Context()); identity != "" {
		actor += ":" + identity
	}
	result, err := h.providerTests.Test(c.Request().Context(), body.TargetID, actor)
	if errors.Is(err, operations.ErrProviderTestTargetChanged) {
		return httperr.New(httperr.CONFLICT, err.Error())
	}
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "provider test configuration unavailable")
	}
	return response.SuccessOK(c, result)
}
