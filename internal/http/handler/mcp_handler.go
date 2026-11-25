package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/mcp"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// MCPHandler serves the MCP Streamable HTTP endpoint at /mcp.
type MCPHandler struct {
	operationObserver    mcp.OperationObserver
	reg                  registry.Registry
	logger               *slog.Logger
	lifecycle            sse.StreamLifecycle
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
}

func (h *MCPHandler) SetOperationObserver(observer mcp.OperationObserver) {
	h.operationObserver = observer
}

// NewMCPHandler constructs a Streamable HTTP MCP handler.
func NewMCPHandler(reg registry.Registry, logger *slog.Logger) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger}
}

func NewMCPHandlerWithLifecycle(reg registry.Registry, logger *slog.Logger, lifecycle sse.StreamLifecycle) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle}
}

// NewMCPHandlerWithLifecycleAndRuntimeConfig constructs a Streamable HTTP MCP
// handler with runtime feature visibility.
func NewMCPHandlerWithLifecycleAndRuntimeConfig(reg registry.Registry, logger *slog.Logger, lifecycle sse.StreamLifecycle, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle, recallFeedbackConfig: recallFeedbackConfig}
}

// HandlePost serves POST /mcp. It accepts a single JSON-RPC payload and returns
// either 202 for notifications, application/json, or a one-shot text/event-stream
// response, depending on the payload and Accept.
func (h *MCPHandler) HandlePost(c *echo.Context) error {
	ctx := c.Request().Context()
	teamID, ok := middleware.GetResolvedTeamID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	principal := middleware.GetPrincipal(ctx)
	if principal == nil {
		return httperr.New(httperr.AUTH_MISSING, "authentication required")
	}
	return h.handleSDKPost(c, teamID, principal)
}

func (h *MCPHandler) handleSDKPost(c *echo.Context, teamID uuid.UUID, principal *middleware.Principal) error {
	request := c.Request()
	requestContext := request.Context()
	request = request.WithContext(rememberapp.WithCallerResponseRequestContext(requestContext, requestContext))
	if err := validateSDKProtocolHeader(request.Header.Get("MCP-Protocol-Version")); err != nil {
		return writeSDKProtocolError(c, http.StatusBadRequest, nil, err.Error())
	}
	accept := request.Header.Get("Accept")
	if !acceptsSDKResponse(accept) {
		return writeSDKProtocolError(c, http.StatusNotAcceptable, nil, "unsupported Accept header")
	}
	// The SDK requires the combined Streamable HTTP Accept value even when the
	// caller requested a single response mode. Preserve the caller's mode for
	// JSONResponse above while normalizing only the transport header.
	request.Header.Set("Accept", "application/json, text/event-stream")
	if err := mcp.PrepareSDKRequest(request); err != nil {
		if errors.Is(err, mcp.ErrSDKRequestTooLarge) {
			return writeSDKProtocolError(c, http.StatusRequestEntityTooLarge, nil, err.Error())
		}
		return httperr.New(httperr.VALIDATION_ERROR, "failed to read request body")
	}
	if id, version, ok := mcp.SDKInitializeVersion(request); ok && !sdkSupportedProtocolVersion(version) {
		return writeSDKProtocolError(c, http.StatusBadRequest, id, "unsupported protocolVersion")
	}

	team := mcp.TeamContext{}
	if resolvedTeam, ok := middleware.GetResolvedTeamContext(request.Context()); ok {
		team.Name = resolvedTeam.Name
		team.Description = resolvedTeam.Description
	}
	server := mcp.NewServerWithScopesTeamContextAndRuntimeConfig(h.reg, teamID.String(), principal.Grants, team, h.logger, h.recallFeedbackConfig)
	server.SetOperationObserver(h.operationObserver)
	serverHandler := server.NewSDKHTTPHandler(!acceptsEventStream(accept))
	work := func(workCtx context.Context) error {
		serverHandler.ServeHTTP(c.Response(), request.WithContext(workCtx))
		return nil
	}
	if h.lifecycle == nil || !acceptsEventStream(accept) {
		return work(request.Context())
	}
	err := h.lifecycle.Start(request.Context(), teamID.String(), discardSSEWriter{}, work)
	if errors.Is(err, sse.ErrTooManyStreams) {
		return httperr.New(httperr.RATE_LIMITED, "too many concurrent streams")
	}
	if errors.Is(err, sse.ErrStreamTerminated) || errors.Is(err, context.Canceled) {
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

var sdkProtocolVersions = map[string]struct{}{
	"2024-11-05": {},
	"2025-03-26": {},
	"2025-06-18": {},
	"2025-11-25": {},
	"2026-07-28": {},
}

func sdkSupportedProtocolVersion(value string) bool {
	_, ok := sdkProtocolVersions[strings.TrimSpace(value)]
	return ok
}

func validateSDKProtocolHeader(value string) error {
	if strings.TrimSpace(value) == "" || sdkSupportedProtocolVersion(value) {
		return nil
	}
	return errors.New("unsupported MCP protocol version")
}

type discardSSEWriter struct{}

func (discardSSEWriter) WriteEvent(string, any) error { return nil }
func (discardSSEWriter) WriteComment(string) error    { return nil }
func (discardSSEWriter) Close() error                 { return nil }

func acceptsSDKResponse(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	for _, part := range strings.Split(value, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mediaType == "application/json" || mediaType == "text/event-stream" {
			return true
		}
	}
	return false
}

func writeSDKProtocolError(c *echo.Context, status int, id json.RawMessage, message string) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error": map[string]any{
			"code":    -32600,
			"message": strings.TrimSpace(message),
		},
	}
	if len(id) > 0 {
		var parsed any
		if json.Unmarshal(id, &parsed) == nil {
			payload["id"] = parsed
		}
	}
	return c.JSON(status, payload)
}

// HandleGet serves GET /mcp as an SSE stream for Streamable HTTP clients.
func (h *MCPHandler) HandleGet(c *echo.Context) error {
	if !acceptsEventStream(c.Request().Header.Get("Accept")) {
		return c.NoContent(http.StatusMethodNotAllowed)
	}
	if h.lifecycle == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "stream lifecycle unavailable")
	}

	teamID, ok := middleware.GetResolvedTeamID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	writer, err := sse.NewSSEWriter(c.Response())
	if err != nil {
		return err
	}

	err = h.lifecycle.Start(c.Request().Context(), teamID.String(), writer, func(ctx context.Context) error {
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
