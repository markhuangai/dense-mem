package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// ToolExecuteHandler executes a registry-backed tool over HTTP.
type ToolExecuteHandler struct {
	reg                  registry.Registry
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
}

// ToolExecuteHandlerInterface is the companion interface for ToolExecuteHandler.
type ToolExecuteHandlerInterface interface {
	Handle(c echo.Context) error
}

var _ ToolExecuteHandlerInterface = (*ToolExecuteHandler)(nil)

// NewToolExecuteHandler constructs a ToolExecuteHandler.
func NewToolExecuteHandler(reg registry.Registry) *ToolExecuteHandler {
	return &ToolExecuteHandler{reg: reg}
}

// NewToolExecuteHandlerWithRuntimeConfig constructs a ToolExecuteHandler with
// runtime feature visibility.
func NewToolExecuteHandlerWithRuntimeConfig(reg registry.Registry, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *ToolExecuteHandler {
	return &ToolExecuteHandler{reg: reg, recallFeedbackConfig: recallFeedbackConfig}
}

// Handle executes POST /api/v1/tools/:name against the shared tool registry.
func (h *ToolExecuteHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	principal := middleware.GetPrincipal(ctx)
	if principal == nil {
		return httperr.New(httperr.AUTH_MISSING, "authentication required")
	}

	name := c.Param("name")
	if name == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "tool name is required")
	}

	tool, ok := h.reg.Get(name)
	if !ok {
		return httperr.New(httperr.NOT_FOUND, "tool not found")
	}
	if !registry.ToolVisible(ctx, tool, h.recallFeedbackConfig) {
		return httperr.New(httperr.NOT_FOUND, "tool not found")
	}
	if !principalCanSeeTool(principal, tool) {
		return httperr.New(httperr.FORBIDDEN, "insufficient scope for tool")
	}
	if tool.Invoke == nil {
		return httperr.New(httperr.INTERNAL_ERROR, "tool not executable")
	}

	args := map[string]any{}
	if c.Request().ContentLength != 0 {
		decoder := json.NewDecoder(c.Request().Body)
		if err := decoder.Decode(&args); err != nil && !errors.Is(err, io.EOF) {
			return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
		}
	}
	if registry.IsEvaluationTool(tool.Name) && registry.HasTenantOverrideArgs(args) {
		return httperr.New(httperr.VALIDATION_ERROR, "evaluation tools do not accept team_id or profile_id")
	}
	if registry.IsV2ContractTool(tool) {
		if err := registry.ValidateV2ContractInput(tool, args, principal.Scopes); err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
	} else {
		registry.StripTenantOverrideArgs(args)
		args = registry.NormalizeInput(tool, args)
		if err := registry.ValidateInput(tool, args); err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
	}

	out, err := tool.Invoke(ctx, profileID.String(), args)
	if err != nil {
		return mapToolExecuteError(err)
	}

	return c.JSON(http.StatusOK, out)
}

// ToolReadHandler returns one tool descriptor from the catalog.
type ToolReadHandler struct {
	reg                  registry.Registry
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
}

// ToolReadHandlerInterface is the companion interface for ToolReadHandler.
type ToolReadHandlerInterface interface {
	Handle(c echo.Context) error
}

var _ ToolReadHandlerInterface = (*ToolReadHandler)(nil)

// NewToolReadHandler constructs a ToolReadHandler.
func NewToolReadHandler(reg registry.Registry) *ToolReadHandler {
	return &ToolReadHandler{reg: reg}
}

// NewToolReadHandlerWithRuntimeConfig constructs a ToolReadHandler with runtime
// feature visibility.
func NewToolReadHandlerWithRuntimeConfig(reg registry.Registry, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *ToolReadHandler {
	return &ToolReadHandler{reg: reg, recallFeedbackConfig: recallFeedbackConfig}
}

// Handle serves GET /api/v1/tools/:id.
func (h *ToolReadHandler) Handle(c echo.Context) error {
	name := c.Param("id")
	if name == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "tool id is required")
	}

	tool, ok := h.reg.Get(name)
	if !ok {
		return httperr.New(httperr.NOT_FOUND, "tool not found")
	}
	if !registry.ToolVisible(c.Request().Context(), tool, h.recallFeedbackConfig) {
		return httperr.New(httperr.NOT_FOUND, "tool not found")
	}

	principal := middleware.GetPrincipal(c.Request().Context())
	if principal != nil {
		if !principalCanSeeTool(principal, tool) {
			return httperr.New(httperr.NOT_FOUND, "tool not found")
		}
	}

	return c.JSON(http.StatusOK, dto.ToolCatalogEntry{
		Name:            tool.Name,
		Description:     tool.Description,
		InputSchema:     tool.InputSchema,
		OutputSchema:    tool.OutputSchema,
		RequiredScopes:  tool.RequiredScopes,
		ContractVersion: tool.ContractVersion,
		FeatureGate:     tool.FeatureGate,
		Visibility:      tool.Visibility,
	})
}

func principalCanSeeTool(principal *middleware.Principal, tool registry.Tool) bool {
	if principal == nil {
		return true
	}

	scopeSet := make(map[string]struct{}, len(principal.Scopes))
	for _, scope := range principal.Scopes {
		scopeSet[scope] = struct{}{}
	}
	for _, needed := range tool.RequiredScopes {
		if _, ok := scopeSet[needed]; !ok {
			return false
		}
	}
	return true
}

func mapToolExecuteError(err error) *httperr.APIError {
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, registry.ErrToolUnavailable):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "tool unavailable")
	case errors.Is(err, registry.ErrToolDisabled):
		return httperr.New(httperr.NOT_FOUND, "tool not found")
	case errors.Is(err, ownership.ErrOwnerMismatch):
		return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
	case errors.Is(err, memoryservice.ErrRememberConflict),
		errors.Is(err, repository.ErrV2IdempotencyConflict), errors.Is(err, repository.ErrV2SourceRevisionConflict):
		return httperr.New(httperr.CONFLICT, "remember conflict")
	case errors.Is(err, embedding.ErrEmbeddingTimeout):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding request timed out")
	case errors.Is(err, embedding.ErrEmbeddingProvider):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding service unavailable")
	case errors.Is(err, embedding.ErrEmbeddingRateLimit):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding service rate limited")
	case errors.Is(err, verifier.ErrVerifierRateLimit):
		return httperr.New(httperr.ErrVerifierRateLimit, "verifier rate limited; retry later")
	case errors.Is(err, verifier.ErrVerifierTimeout):
		return httperr.New(httperr.ErrVerifierTimeout, "verifier request timed out")
	case errors.Is(err, verifier.ErrVerifierProvider):
		return httperr.New(httperr.ErrVerifierProvider, "verifier provider error")
	case errors.Is(err, verifier.ErrVerifierMalformedResponse):
		return httperr.New(httperr.ErrVerifierMalformedResponse, "verifier returned a malformed response")
	case strings.Contains(err.Error(), "invalid input"),
		strings.Contains(err.Error(), "is required"),
		strings.Contains(err.Error(), "invalid cursor"),
		strings.Contains(err.Error(), "validation"):
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "tool execution failed")
	}
}
