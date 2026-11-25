package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// The SDK treats every stateless POST as a session, so its routine lifecycle belongs at debug level.
type sdkLifecycleLogHandler struct {
	delegate slog.Handler
}

func (h sdkLifecycleLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.delegate.Enabled(ctx, level)
}

func (h sdkLifecycleLogHandler) Handle(ctx context.Context, record slog.Record) error {
	level := record.Level
	if level == slog.LevelInfo {
		switch record.Message {
		case "server connecting", "server session connected", "session initialized", "server session disconnected":
			level = slog.LevelDebug
		}
	}
	if !h.delegate.Enabled(ctx, level) {
		return nil
	}
	clean := slog.NewRecord(record.Time, level, sdkMessageClass(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if attr := sdkLogAttr(attr); attr.Key != "" {
			clean.AddAttrs(attr)
		}
		return true
	})
	return h.delegate.Handle(ctx, clean)
}

func (h sdkLifecycleLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if attr := sdkLogAttr(attr); attr.Key != "" {
			clean = append(clean, attr)
		}
	}
	return sdkLifecycleLogHandler{delegate: h.delegate.WithAttrs(clean)}
}

func (h sdkLifecycleLogHandler) WithGroup(name string) slog.Handler {
	return sdkLifecycleLogHandler{delegate: h.delegate.WithGroup(name)}
}

func newSDKLogger(delegate *slog.Logger) *slog.Logger {
	if delegate == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(sdkLifecycleLogHandler{delegate: delegate.Handler()})
}

func sdkMessageClass(message string) string {
	switch message {
	case "server connecting", "server session connected", "session initialized", "server session disconnected",
		"resource updated notification sent", "resource subscribed", "resource unsubscribed", "server run start", "server session ended":
		return message
	default:
		return "mcp_sdk_event"
	}
}

func sdkLogAttr(attr slog.Attr) slog.Attr {
	switch attr.Key {
	case "session_id":
		return attr
	case "error":
		return slog.String("error", "mcp SDK operation failed")
	default:
		return slog.Attr{}
	}
}

func (s *Server) logSDKToolOutcome(ctx context.Context, name string, started time.Time, result *sdkmcp.CallToolResult, err error) {
	if s.logger == nil {
		return
	}
	outcome := sdkToolApplicationOutcome(ctx, result, err)
	attrs := []slog.Attr{
		slog.String("tool", name),
		slog.String("application_outcome", outcome),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
	}
	if result != nil {
		attrs = appendSDKApplicationRefs(attrs, result.StructuredContent)
	}
	logCtx := context.Background()
	if ctx != nil {
		logCtx = context.WithoutCancel(ctx)
	}
	if outcome == "success" {
		s.logger.LogAttrs(logCtx, slog.LevelInfo, "mcp_tool_outcome", attrs...)
		return
	}
	if err == nil {
		err = errors.New(outcome)
	}
	s.logger.LogAttrs(logCtx, slog.LevelError, "mcp_tool_outcome", append(attrs, slog.Any("error", err))...)
}

func sdkToolApplicationOutcome(ctx context.Context, result *sdkmcp.CallToolResult, err error) string {
	if err != nil {
		if ctx != nil && ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return "cancelled"
		}
		return "rpc_error"
	}
	if result == nil {
		if ctx != nil && ctx.Err() != nil {
			return "cancelled"
		}
		return "missing_result"
	}
	if result.IsError {
		if ctx != nil && ctx.Err() != nil && sdkToolResultWasCancelled(result) {
			return "cancelled"
		}
		return "tool_error"
	}
	return "success"
}

func sdkToolResultWasCancelled(result *sdkmcp.CallToolResult) bool {
	if result == nil || !result.IsError {
		return false
	}
	fields, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return false
	}
	if reasonCode, ok := fields["reason_code"].(string); ok && reasonCode == "request_cancelled" {
		return true
	}
	errorsValue, ok := fields["errors"]
	if !ok {
		return false
	}
	switch values := errorsValue.(type) {
	case []any:
		for _, value := range values {
			if errorFields, ok := value.(map[string]any); ok && toolErrorFieldsWereCancelled(errorFields) {
				return true
			}
		}
	case []map[string]any:
		for _, errorFields := range values {
			if toolErrorFieldsWereCancelled(errorFields) {
				return true
			}
		}
	}
	return false
}

func toolErrorFieldsWereCancelled(fields map[string]any) bool {
	for _, key := range []string{"code", "reason_code"} {
		if value, ok := fields[key].(string); ok && value == "request_cancelled" {
			return true
		}
	}
	return false
}

func appendSDKApplicationRefs(attrs []slog.Attr, value any) []slog.Attr {
	var fields map[string]any
	switch typed := value.(type) {
	case map[string]any:
		fields = typed
	case json.RawMessage:
		_ = json.Unmarshal(typed, &fields)
	case []byte:
		_ = json.Unmarshal(typed, &fields)
	}
	for _, key := range []string{"submission_id", "attempt_id", "canonical_attempt_id"} {
		if text, ok := fields[key].(string); ok && strings.TrimSpace(text) != "" {
			attrs = append(attrs, slog.String(key, text))
		}
	}
	return attrs
}

// NewSDKHTTPHandler creates a stateless official-SDK transport backed by the
// request-scoped registry, authorization, and prompt catalog.
func (s *Server) NewSDKHTTPHandler(jsonResponse bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, prepared := req.Context().Value(sdkEnvelopeContextKey{}).(*sdkEnvelope); !prepared {
			if err := PrepareSDKRequest(req); err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, ErrSDKRequestTooLarge) {
					status = http.StatusRequestEntityTooLarge
				}
				http.Error(w, err.Error(), status)
				return
			}
		}
		req = req.WithContext(context.WithValue(req.Context(), sdkProgressHTTPContextKey{}, sdkProgressHTTP{
			controller: http.NewResponseController(w), sse: !jsonResponse,
			protocol: req.Header.Get("MCP-Protocol-Version"),
		}))
		if s.writeSDKToolLookupError(w, req, jsonResponse) {
			return
		}
		// The SDK may look up the server more than once while processing one request.
		var server *sdkmcp.Server
		transport := sdkmcp.NewStreamableHTTPHandler(func(req *http.Request) *sdkmcp.Server {
			if server == nil {
				server = s.newSDKServer(req.Context())
			}
			return server
		}, &sdkmcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 jsonResponse,
			MaxRequestBodyBytes:          maxSDKRequestBytes,
			PropagateRequestCancellation: true,
		})
		transport.ServeHTTP(w, req)
	})
}

type sdkToolDispatchContextKey struct{}
type sdkEnvelopeContextKey struct{}

const maxSDKRequestBytes = 4 << 20

var ErrSDKRequestTooLarge = errors.New("request body exceeds limit")

type sdkEnvelope struct {
	JSONRPC         string          `json:"jsonrpc"`
	ID              json.RawMessage `json:"id"`
	Method          string          `json:"method"`
	Params          json.RawMessage `json:"params"`
	name            string
	protocolVersion string
	progressToken   json.RawMessage
}

// PrepareSDKRequest retains one bounded HTTP body for the SDK and shares its
// envelope with the application protocol checks.
func PrepareSDKRequest(req *http.Request) error {
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return nil
	}
	payload, err := io.ReadAll(io.LimitReader(req.Body, maxSDKRequestBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxSDKRequestBytes {
		return ErrSDKRequestTooLarge
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	var envelope *sdkEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope == nil || envelope.JSONRPC != "2.0" {
		envelope = nil
	}
	if envelope != nil {
		switch envelope.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(envelope.Params, &params) == nil {
				envelope.protocolVersion = params.ProtocolVersion
			}
		case "tools/call":
			var params struct {
				Name string          `json:"name"`
				Meta json.RawMessage `json:"_meta"`
			}
			if json.Unmarshal(envelope.Params, &params) == nil {
				envelope.name = params.Name
				var metadata map[string]json.RawMessage
				if json.Unmarshal(params.Meta, &metadata) == nil {
					envelope.progressToken = metadata["progressToken"]
				}
			}
		}
	}
	ctx := context.WithValue(req.Context(), sdkEnvelopeContextKey{}, envelope)
	if envelope != nil && envelope.Method == "tools/call" && isSDKJSONRequest(req) {
		ctx = context.WithValue(ctx, sdkToolDispatchContextKey{}, len(envelope.ID) > 0 && sdkRPCIDValid(envelope.ID))
		ctx = context.WithValue(ctx, sdkProgressMetadataContextKey{}, sdkProgressMetadata{
			token: envelope.progressToken, requestID: envelope.ID,
		})
	}
	*req = *req.WithContext(ctx)
	return nil
}

func isSDKJSONRequest(req *http.Request) bool {
	contentType, _, _ := strings.Cut(req.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(contentType), "application/json")
}

func SDKInitializeVersion(req *http.Request) (json.RawMessage, string, bool) {
	envelope, _ := req.Context().Value(sdkEnvelopeContextKey{}).(*sdkEnvelope)
	if envelope == nil || envelope.Method != "initialize" || strings.TrimSpace(envelope.protocolVersion) == "" {
		return nil, "", false
	}
	return envelope.ID, envelope.protocolVersion, true
}

func sdkToolDispatchRequested(ctx context.Context) bool {
	value, ok := ctx.Value(sdkToolDispatchContextKey{}).(bool)
	if !ok {
		// Direct handler callers and transports that have already validated the
		// JSON-RPC request are counted by default.
		return true
	}
	return value
}

func (s *Server) writeSDKToolLookupError(w http.ResponseWriter, req *http.Request, jsonResponse bool) bool {
	if req == nil || req.Body == nil || req.Method != http.MethodPost ||
		!isSDKJSONRequest(req) || !sdkAcceptsStreamableHTTP(req.Header.Get("Accept")) {
		return false
	}
	envelope, _ := req.Context().Value(sdkEnvelopeContextKey{}).(*sdkEnvelope)
	if envelope == nil || envelope.Method != "tools/call" || strings.TrimSpace(envelope.name) == "" {
		return false
	}
	if !sdkStandardHeadersMatch(req, envelope.Method, envelope.name) {
		return false
	}
	// JSON-RPC notifications never receive a response, including when the
	// requested tool is hidden or unknown. Leave them to the SDK transport so
	// it can preserve the notification status code and empty body.
	if len(envelope.ID) == 0 {
		return false
	}
	if !sdkRPCIDValid(envelope.ID) {
		return false
	}
	tool, ok := s.registry.Get(envelope.name)
	policy := registry.ResolveRuntimeToolPolicy(req.Context(), s.runtimeToolPolicy, tool)
	visible := ok && registry.ToolVisible(req.Context(), tool, policy)
	if visible && s.canUseTool(tool) {
		return false
	}
	code, message := errCodeMethodNotFound, "tool not found: "+boundedRPCText(envelope.name)
	data := registry.ActionableInvalidInputData(req.Context(), envelope.name, "tool_not_available", "The requested tool is not available for this connection.", "Refresh tools/list and call an available tool.")
	scopeDenied := visible && !s.canUseTool(tool)
	if scopeDenied {
		code, message = errCodeToolFailure, "insufficient scope for tool"
		data = registry.ActionableAuthorizationData(req.Context(), envelope.name)
	}
	domain.RecordMCPToolOutcome(req.Context(), "rpc_error")
	s.logSDKToolLookupFailure(req.Context(), tool, visible, scopeDenied, code)
	response := map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": code, "message": message, "data": data}}
	if len(envelope.ID) > 0 {
		response["id"] = json.RawMessage(envelope.ID)
	}
	encoded, _ := json.Marshal(response)
	if !jsonResponse && strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " + string(encoded) + "\n\n"))
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}
	return true
}

func (s *Server) logSDKToolLookupFailure(ctx context.Context, tool registry.Tool, visible, scopeDenied bool, code int) {
	if s.logger == nil {
		return
	}
	reason := "tool_not_available"
	if scopeDenied {
		reason = "scope_denied"
	}
	attrs := []slog.Attr{
		slog.String("application_outcome", "rpc_error"),
		slog.Int("error_code", code),
		slog.String("lookup_reason", reason),
	}
	if visible && strings.TrimSpace(tool.Name) != "" {
		attrs = append(attrs, slog.String("tool", tool.Name))
	}
	logCtx := context.Background()
	if ctx != nil {
		logCtx = context.WithoutCancel(ctx)
	}
	s.logger.LogAttrs(logCtx, slog.LevelError, "mcp_tool_outcome",
		append(attrs, slog.String("error", "mcp tool lookup rejected"))...)
}

func sdkRPCIDValid(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, float64:
		return true
	default:
		return false
	}
}

func sdkAcceptsStreamableHTTP(value string) bool {
	jsonOK, streamOK := false, false
	for _, part := range strings.Split(value, ",") {
		switch strings.TrimSpace(strings.SplitN(part, ";", 2)[0]) {
		case "application/json":
			jsonOK = true
		case "text/event-stream":
			streamOK = true
		}
	}
	return jsonOK && streamOK
}

func sdkStandardHeadersMatch(req *http.Request, method, name string) bool {
	version := strings.TrimSpace(req.Header.Get("Mcp-Protocol-Version"))
	if version < "2026-07-28" {
		return true
	}
	if req.Header.Get("Mcp-Method") != method {
		return false
	}
	return req.Header.Get("Mcp-Name") == name
}

func (s *Server) newSDKServer(ctx context.Context) *sdkmcp.Server {
	capabilities := &sdkmcp.ServerCapabilities{
		Tools: &sdkmcp.ToolCapabilities{},
	}
	if len(s.prompts.List()) > 0 {
		capabilities.Prompts = &sdkmcp.PromptCapabilities{}
	}
	implementation := &sdkmcp.Implementation{
		Name:        s.serverName(),
		Version:     ServerVersion,
		Description: s.serverDescription(),
	}
	if s.team.Name != "" {
		implementation.Title = s.teamDisplayName()
	}
	server := sdkmcp.NewServer(implementation, &sdkmcp.ServerOptions{
		Instructions: s.instructions(),
		Capabilities: capabilities,
		SchemaCache:  sdkmcp.NewSchemaCache(),
		Logger:       newSDKLogger(s.logger),
	})

	tools := s.registry.List()
	policy := registry.ResolveRuntimeToolPolicy(ctx, s.runtimeToolPolicy, tools...)
	for _, tool := range tools {
		if !registry.ToolVisible(ctx, tool, policy) || !s.canUseTool(tool) {
			continue
		}
		inputSchema := tool.InputSchema
		if inputSchema == nil {
			inputSchema = map[string]any{"type": "object"}
		}
		server.AddTool(&sdkmcp.Tool{
			Name:         tool.Name,
			Description:  s.toolDescription(tool.Description),
			InputSchema:  inputSchema,
			OutputSchema: tool.OutputSchema,
		}, s.sdkToolHandler(tool.Name))
	}

	for _, prompt := range s.prompts.List() {
		prompt := prompt
		arguments := make([]*sdkmcp.PromptArgument, 0, len(prompt.Arguments))
		for _, argument := range prompt.Arguments {
			argument := argument
			arguments = append(arguments, &sdkmcp.PromptArgument{
				Name:        argument.Name,
				Description: argument.Description,
				Required:    argument.Required,
			})
		}
		server.AddPrompt(&sdkmcp.Prompt{
			Name:        prompt.Name,
			Title:       prompt.Title,
			Description: prompt.Description,
			Arguments:   arguments,
		}, func(ctx context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			args := map[string]string{}
			if req != nil && req.Params != nil {
				args = req.Params.Arguments
			}
			_, rendered, err := s.prompts.Render(prompt.Name, args)
			if err != nil {
				return nil, &sdkjsonrpc.Error{Code: errCodeInvalidParams, Message: boundedRPCText(err.Error())}
			}
			return &sdkmcp.GetPromptResult{
				Description: prompt.Description,
				Messages: []*sdkmcp.PromptMessage{{
					Role:    sdkmcp.Role("user"),
					Content: &sdkmcp.TextContent{Text: rendered},
				}},
			}, nil
		})
	}
	return server
}

func (s *Server) sdkToolHandler(name string) sdkmcp.ToolHandler {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (result *sdkmcp.CallToolResult, err error) {
		started := time.Now()
		defer func() {
			if sdkToolDispatchRequested(ctx) {
				if s.operationObserver != nil {
					s.operationObserver.ObserveOperation(ctx, name, started, sdkToolApplicationOutcome(ctx, result, err))
				}
				domain.RecordMCPToolOutcome(ctx, sdkToolApplicationOutcome(ctx, result, err))
				s.logSDKToolOutcome(ctx, name, started, result, err)
			}
		}()
		return s.invokeSDKTool(name, ctx, req)
	}
}

func (s *Server) invokeSDKTool(name string, ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	var args map[string]any
	if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			data, _ := json.Marshal(registry.ActionableInvalidInputData(ctx, name, "invalid_json", "The tool arguments are not valid JSON.", "Correct the JSON arguments and submit the request again."))
			return nil, &sdkjsonrpc.Error{Code: errCodeInvalidParams, Message: "invalid params", Data: data}
		}
	}
	ctx, stopProgress := s.withSDKRememberProgress(ctx, name, req)
	defer stopProgress()
	result, rpcErr := s.invokeTool(ctx, name, args)
	if rpcErr != nil {
		tool, toolExists := s.registry.Get(name)
		if rpcErr.Code == errCodeToolFailure && rpcErr.Data != nil && toolExists && s.isOperationalTool(name, tool) {
			return sdkCallToolErrorResult(rpcErr.Data)
		}
		return nil, sdkRPCError(rpcErr)
	}
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		tool, toolExists := s.registry.Get(name)
		if toolExists && s.isOperationalTool(name, tool) {
			return sdkCallToolErrorResult(registry.ActionableToolUnavailableData(ctx, name))
		}
		return nil, &sdkjsonrpc.Error{Code: errCodeToolFailure, Message: "tool result serialization failed"}
	}
	text, ok := content[0]["text"].(string)
	if !ok {
		tool, toolExists := s.registry.Get(name)
		if toolExists && s.isOperationalTool(name, tool) {
			return sdkCallToolErrorResult(registry.ActionableToolUnavailableData(ctx, name))
		}
		return nil, &sdkjsonrpc.Error{Code: errCodeToolFailure, Message: "tool result serialization failed"}
	}
	structuredContent := result["structuredContent"]
	isError, _ := result["isError"].(bool)
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
		StructuredContent: structuredContent,
		IsError:           isError,
	}, nil
}

func (s *Server) isOperationalTool(name string, tool registry.Tool) bool {
	return registry.IsContractTool(tool)
}

func sdkCallToolErrorResult(data any) (*sdkmcp.CallToolResult, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, &sdkjsonrpc.Error{Code: errCodeToolFailure, Message: "tool result serialization failed"}
	}
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(payload)}},
		StructuredContent: data,
		IsError:           true,
	}, nil
}

func sdkRPCError(value *rpcError) error {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value.Data)
	if value.Data == nil {
		data = nil
	}
	return &sdkjsonrpc.Error{Code: int64(value.Code), Message: value.Message, Data: data}
}

func sdkProtocolError(message string) error {
	return &sdkjsonrpc.Error{Code: errCodeInvalidRequest, Message: strings.TrimSpace(message)}
}
