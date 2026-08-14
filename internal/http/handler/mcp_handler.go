package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
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
	dreams               registry.DreamingConfigProvider
	transport            string
}

// MCPHandlerInterface is the companion interface for MCPHandler.
type MCPHandlerInterface interface {
	HandlePost(c echo.Context) error
	HandleGet(c echo.Context) error
}

var _ MCPHandlerInterface = (*MCPHandler)(nil)

// NewMCPHandler constructs a Streamable HTTP MCP handler.
func NewMCPHandler(reg registry.Registry, logger observability.LogProvider) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, transport: "legacy"}
}

func NewMCPHandlerWithLifecycle(reg registry.Registry, logger observability.LogProvider, lifecycle sse.StreamLifecycle) *MCPHandler {
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle, transport: "legacy"}
}

// NewMCPHandlerWithLifecycleAndRuntimeConfig constructs a Streamable HTTP MCP
// handler with runtime feature visibility.
func NewMCPHandlerWithLifecycleAndRuntimeConfig(reg registry.Registry, logger observability.LogProvider, lifecycle sse.StreamLifecycle, recallFeedbackConfig registry.RecallFeedbackConfigProvider, dreams ...registry.DreamingConfigProvider) *MCPHandler {
	return NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(reg, logger, lifecycle, recallFeedbackConfig, "legacy", dreams...)
}

// NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport constructs the MCP
// handler with a boot-only transport selector. The legacy constructor remains
// available for focused compatibility tests and rollback.
func NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(reg registry.Registry, logger observability.LogProvider, lifecycle sse.StreamLifecycle, recallFeedbackConfig registry.RecallFeedbackConfigProvider, transport string, dreams ...registry.DreamingConfigProvider) *MCPHandler {
	var dreamConfig registry.DreamingConfigProvider
	if len(dreams) > 0 {
		dreamConfig = dreams[0]
	}
	if transport != "sdk" {
		transport = "legacy"
	}
	return &MCPHandler{reg: reg, logger: logger, lifecycle: lifecycle, recallFeedbackConfig: recallFeedbackConfig, dreams: dreamConfig, transport: transport}
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
	if h.transport == "sdk" {
		return h.handleSDKPost(c, profileID, principal)
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
	server := mcp.NewServerWithScopesTeamContextAndRuntimeConfig(h.reg, profileID.String(), principal.Scopes, team, h.logger, h.recallFeedbackConfig, h.dreams)
	response := server.HandlePayloadResult(ctx, payload)
	if !response.Respond {
		return c.NoContent(http.StatusAccepted)
	}

	if acceptsEventStream(c.Request().Header.Get("Accept")) {
		return writeMCPSSE(c, response.Payload)
	}

	return c.Blob(http.StatusOK, "application/json", response.Payload)
}

func (h *MCPHandler) handleSDKPost(c echo.Context, profileID uuid.UUID, principal *middleware.Principal) error {
	request := c.Request()
	if !sdkJSONContentType(request.Header.Get("Content-Type")) {
		return writeSDKProtocolError(c, http.StatusUnsupportedMediaType, nil, "unsupported Content-Type")
	}
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
	if request.Body != nil {
		payload, err := io.ReadAll(io.LimitReader(request.Body, 4<<20+1))
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "failed to read request body")
		}
		if len(payload) > 4<<20 {
			return c.NoContent(http.StatusRequestEntityTooLarge)
		}
		request.Body = io.NopCloser(bytes.NewReader(payload))
		if id, version, ok := sdkInitializePayload(payload); ok && !sdkSupportedProtocolVersion(version) {
			return writeSDKProtocolError(c, http.StatusBadRequest, id, "unsupported protocolVersion")
		}
		if sdkHeaderProtocolVersion(request.Header.Get("MCP-Protocol-Version")) == "2026-07-28" {
			if err := populateSDKStandardHeaders(request, payload); err != nil {
				return writeSDKProtocolError(c, http.StatusBadRequest, nil, err.Error())
			}
		}
	}

	team := mcp.TeamContext{}
	if resolvedTeam, ok := middleware.GetResolvedTeamContext(request.Context()); ok {
		team.Name = resolvedTeam.Name
		team.Description = resolvedTeam.Description
	}
	server := mcp.NewServerWithScopesTeamContextAndRuntimeConfig(h.reg, profileID.String(), principal.Scopes, team, h.logger, h.recallFeedbackConfig, h.dreams)
	serverHandler := server.NewSDKHTTPHandler(!acceptsEventStream(accept))
	serverHandler.ServeHTTP(c.Response(), request)
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

func sdkHeaderProtocolVersion(value string) string {
	return strings.TrimSpace(value)
}

// populateSDKStandardHeaders supplies the required 2026 protocol routing
// headers when a client has sent a valid JSON-RPC request but omitted them.
// Existing values are validated rather than overwritten so a mismatched
// header cannot be used to route a different method or tool.
func populateSDKStandardHeaders(request *http.Request, payload []byte) error {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.JSONRPC != "2.0" || strings.TrimSpace(envelope.Method) == "" {
		return nil
	}
	method := strings.TrimSpace(envelope.Method)
	if current := strings.TrimSpace(request.Header.Get("Mcp-Method")); current != "" && current != method {
		return errors.New("Mcp-Method header does not match request method")
	}
	request.Header.Set("Mcp-Method", method)
	if method != "tools/call" && method != "prompts/get" && method != "resources/read" {
		return nil
	}
	var params struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	if err := json.Unmarshal(envelope.Params, &params); err != nil {
		return errors.New("invalid MCP request params")
	}
	name := params.Name
	if method == "resources/read" {
		name = params.URI
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("Mcp-Name is required for named MCP methods")
	}
	if current := strings.TrimSpace(request.Header.Get("Mcp-Name")); current != "" && current != name {
		return errors.New("Mcp-Name header does not match request name")
	}
	request.Header.Set("Mcp-Name", name)
	return nil
}

func sdkInitializePayload(payload []byte) (json.RawMessage, string, bool) {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.Method != "initialize" {
		return nil, "", false
	}
	return envelope.ID, envelope.Params.ProtocolVersion, strings.TrimSpace(envelope.Params.ProtocolVersion) != ""
}

func acceptsSDKResponse(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	for _, supported := range []string{"application/json", "text/event-stream"} {
		for _, part := range strings.Split(value, ",") {
			mediaType, accepted := acceptedMediaType(part)
			if accepted && mediaTypeMatches(mediaType, supported) {
				return true
			}
		}
	}
	return false
}

func sdkJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func mediaTypeMatches(mediaRange, supported string) bool {
	if mediaRange == "*/*" {
		return true
	}
	if strings.HasSuffix(mediaRange, "/*") {
		prefix := strings.TrimSuffix(mediaRange, "/*")
		return prefix != "" && strings.HasPrefix(supported, prefix+"/")
	}
	return mediaRange == supported
}

func writeSDKProtocolError(c echo.Context, status int, id json.RawMessage, message string) error {
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
		mediaType, accepted := acceptedMediaType(part)
		if accepted && mediaType != "*/*" && mediaTypeMatches(mediaType, "text/event-stream") {
			return true
		}
	}
	return false
}

func acceptedMediaType(part string) (string, bool) {
	parameters := strings.Split(part, ";")
	mediaType := strings.ToLower(strings.TrimSpace(parameters[0]))
	if mediaType == "" {
		return "", false
	}
	for _, parameter := range parameters[1:] {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		quality, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(value), `"`), 64)
		if err != nil || quality <= 0 || quality > 1 {
			return "", false
		}
	}
	return mediaType, true
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
