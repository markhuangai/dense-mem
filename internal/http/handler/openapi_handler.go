package handler

import (
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/openapi"
)

// OpenAPIHandler serves the generated OpenAPI document for a given variant.
//
// The handler does NOT decide authorization — the router binds the AI-safe
// variant and full runtime variant to protected routes, so whoever reaches
// this handler is already authorized for the requested variant.
type OpenAPIHandler struct {
	gen       openapi.Generator
	variant   openapi.SpecVariant
	cacheOnce sync.Once
	cacheSpec map[string]any
	cacheErr  error
}

// OpenAPIHandlerInterface is the companion interface for OpenAPIHandler.
type OpenAPIHandlerInterface interface {
	Handle(c echo.Context) error
}

var _ OpenAPIHandlerInterface = (*OpenAPIHandler)(nil)

// NewOpenAPIHandler constructs a handler bound to a specific spec variant.
func NewOpenAPIHandler(gen openapi.Generator, variant openapi.SpecVariant) *OpenAPIHandler {
	return &OpenAPIHandler{gen: gen, variant: variant}
}

// Handle returns the OpenAPI document as JSON.
func (h *OpenAPIHandler) Handle(c echo.Context) error {
	h.cacheOnce.Do(func() {
		h.cacheSpec, h.cacheErr = h.gen.Generate(h.variant)
	})
	if h.cacheErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate openapi spec")
	}
	return c.JSON(http.StatusOK, h.cacheSpec)
}
