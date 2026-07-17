package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// ToolCatalogHandler serves GET /api/v1/tools.
//
// The handler maps each registered Tool into a public ToolCatalogEntry DTO.
// Only tool metadata travels over the wire; the bound invoker function and any
// internal Go types stay on the server side (AC-32: no internal type leakage).
type ToolCatalogHandler struct {
	reg                  registry.Registry
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
}

// ToolCatalogHandlerInterface is the companion interface for ToolCatalogHandler.
type ToolCatalogHandlerInterface interface {
	Handle(c echo.Context) error
}

var _ ToolCatalogHandlerInterface = (*ToolCatalogHandler)(nil)

// NewToolCatalogHandler constructs a ToolCatalogHandler over the given registry.
func NewToolCatalogHandler(reg registry.Registry) *ToolCatalogHandler {
	return &ToolCatalogHandler{reg: reg}
}

// NewToolCatalogHandlerWithRuntimeConfig constructs a ToolCatalogHandler with
// runtime feature visibility.
func NewToolCatalogHandlerWithRuntimeConfig(reg registry.Registry, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *ToolCatalogHandler {
	return &ToolCatalogHandler{reg: reg, recallFeedbackConfig: recallFeedbackConfig}
}

// Handle returns the full catalog of registered tools.
func (h *ToolCatalogHandler) Handle(c echo.Context) error {
	principal := middleware.GetPrincipal(c.Request().Context())
	tools := h.reg.List()
	entries := make([]dto.ToolCatalogEntry, 0, len(tools))
	for _, t := range tools {
		if !registry.ToolVisible(c.Request().Context(), t, h.recallFeedbackConfig) {
			continue
		}
		if principal != nil {
			if !principalCanSeeTool(principal, t) {
				continue
			}
		}
		entries = append(entries, dto.ToolCatalogEntry{
			Name:            t.Name,
			Description:     t.Description,
			InputSchema:     t.InputSchema,
			OutputSchema:    t.OutputSchema,
			RequiredScopes:  t.RequiredScopes,
			ContractVersion: t.ContractVersion,
			FeatureGate:     t.FeatureGate,
			Visibility:      t.Visibility,
		})
	}
	return c.JSON(http.StatusOK, dto.ToolCatalogResponse{Tools: entries})
}
