package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/mcp"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// MCPHandler serves the MCP Streamable HTTP endpoint at /mcp.
type MCPHandler struct {
	reg                  registry.Registry
	logger               observability.LogProvider
	lifecycle            sse.StreamLifecycle
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
}

// MCPHandlerInterface is the companion interface for MCPHandler.
type MCPHandlerInterface interface {
	HandlePost(c echo.Context) error
	HandleGet(c echo.Context) error
}

var _ MCPHandlerInterface = (*MCPHandler)(nil)

// NewMCPHandler constructs a Streamable HTTP MCP handler.
func NewMCPHandler(reg registry.Registry, logger observability.LogProvider) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger}
}

func NewMCPHandlerWithLifecycle(reg registry.Registry, logger observability.LogProvider, lifecycle sse.StreamLifecycle) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle}
}

// NewMCPHandlerWithLifecycleAndRuntimeConfig constructs a Streamable HTTP MCP
// handler with runtime feature visibility.
func NewMCPHandlerWithLifecycleAndRuntimeConfig(reg registry.Registry, logger observability.LogProvider, lifecycle sse.StreamLifecycle, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle, recallFeedbackConfig: recallFeedbackConfig}
}

// HandlePost serves POST /mcp. It accepts a single JSON-RPC payload and returns
// either 202 for notifications, application/json, or a one-shot text/event-stream
// response, depending on the payload and Accept.
func (h *MCPHandler) HandlePost(c echo.Context) error {
	ctx := c.Request().Context()
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	principal := middleware.GetPrincipal(ctx)
	if principal == nil {
		return httperr.New(httperr.AUTH_MISSING, "authentication required")
	}

	payload, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "failed to read request body")
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return httperr.New(httperr.VALIDATION_ERROR, "request body is required")
	}

	team := mcp.TeamContext{}
	if resolvedTeam, ok := middleware.GetResolvedTeamContext(ctx); ok {
		team.Name = resolvedTeam.Name
		team.Description = resolvedTeam.Description
	}
	server := mcp.NewServerWithScopesTeamContextAndRuntimeConfig(h.reg, profileID.String(), principal.Scopes, team, h.logger, h.recallFeedbackConfig)
	response := server.HandlePayloadResult(ctx, payload)
	if !response.Respond {
		return c.NoContent(http.StatusAccepted)
	}

	if acceptsEventStream(c.Request().Header.Get("Accept")) {
		return writeMCPSSE(c, response.Payload)
	}

	return c.Blob(http.StatusOK, "application/json", response.Payload)
}

// HandleGet serves GET /mcp as an SSE stream for Streamable HTTP clients.
func (h *MCPHandler) HandleGet(c echo.Context) error {
	if !acceptsEventStream(c.Request().Header.Get("Accept")) {
		return c.NoContent(http.StatusMethodNotAllowed)
	}
	if h.lifecycle == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "stream lifecycle unavailable")
	}

	profileID, ok := middleware.GetResolvedProfileID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	writer, err := sse.NewSSEWriter(c.Response())
	if err != nil {
		return err
	}

	err = h.lifecycle.Start(c.Request().Context(), profileID.String(), writer, func(ctx context.Context) error {
		if err := writer.WriteComment("dense-mem MCP stream ready"); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	if errors.Is(err, sse.ErrTooManyStreams) {
		return httperr.New(httperr.RATE_LIMITED, "too many concurrent streams")
	}
	if errors.Is(err, sse.ErrStreamTerminated) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func acceptsEventStream(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mediaType == "text/event-stream" {
			return true
		}
	}
	return false
}

func writeMCPSSE(c echo.Context, payload []byte) error {
	headers := c.Response().Header()
	headers.Set(echo.HeaderContentType, "text/event-stream")
	headers.Set(echo.HeaderCacheControl, "no-cache")
	headers.Set(echo.HeaderConnection, "keep-alive")
	c.Response().WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(c.Response(), "event: message\ndata: %s\n\n", payload); err != nil {
		return err
	}
	flushSSE(c)
	return nil
}

func writeSSEComment(c echo.Context, text string) error {
	if _, err := fmt.Fprintf(c.Response(), ": %s\n\n", text); err != nil {
		return err
	}
	flushSSE(c)
	return nil
}

func flushSSE(c echo.Context) {
	if flusher, ok := c.Response().Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
