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
	if record.Level == slog.LevelInfo {
		switch record.Message {
		case "server connecting", "server session connected", "session initialized", "server session disconnected":
			record.Level = slog.LevelDebug
		}
	}
	if !h.delegate.Enabled(ctx, record.Level) {
		return nil
	}
	return h.delegate.Handle(ctx, record)
}

func (h sdkLifecycleLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return sdkLifecycleLogHandler{delegate: h.delegate.WithAttrs(attrs)}
}

func (h sdkLifecycleLogHandler) WithGroup(name string) slog.Handler {
	return sdkLifecycleLogHandler{delegate: h.delegate.WithGroup(name)}
}

func newSDKLogger(delegate *slog.Logger) *slog.Logger {
	return slog.New(sdkLifecycleLogHandler{delegate: delegate.Handler()})
}

// sdkRootLogHandler adapts the official SDK's slog surface to the injected
// transport logger. The SDK accepts a *slog.Logger, but constructing one from
// slog.Default would bypass request attribution and the process root.
type sdkRootLogHandler struct {
	logger Logger
	attrs  []LogField
	group  string
}

func (h sdkRootLogHandler) Enabled(context.Context, slog.Level) bool {
	return h.logger != nil
}

func (h sdkRootLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.logger == nil {
		return nil
	}
	level := record.Level
	if level == slog.LevelInfo {
		switch record.Message {
		case "server connecting", "server session connected", "session initialized", "server session disconnected":
			level = slog.LevelDebug
		}
	}
	attrs := append([]LogField(nil), h.attrs...)
	hasError := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "error" {
			hasError = true
			return true
		}
		if attr.Key != "session_id" {
			return true
		}
		key := attr.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs = append(attrs, LogField{Key: key, Value: slogValueAny(attr.Value)})
		return true
	})
	message := sdkMessageClass(record.Message)
	if hasError {
		sdkLogError(ctx, h.logger, level, message, errors.New("mcp SDK operation failed"), attrs...)
		return nil
	}
	sdkLog(ctx, h.logger, level, message, attrs...)
	return nil
}

func (h sdkRootLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fields := append([]LogField(nil), h.attrs...)
	for _, attr := range attrs {
		if attr.Key != "session_id" {
			continue
		}
		key := attr.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		fields = append(fields, LogField{Key: key, Value: slogValueAny(attr.Value)})
	}
	return sdkRootLogHandler{logger: h.logger, attrs: fields, group: h.group}
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

func (h sdkRootLogHandler) WithGroup(name string) slog.Handler {
	group := strings.TrimSpace(name)
	if h.group != "" && group != "" {
		group = h.group + "." + group
	}
	return sdkRootLogHandler{logger: h.logger, attrs: append([]LogField(nil), h.attrs...), group: group}
}

func newSDKRootLogger(logger Logger) *slog.Logger {
	return slog.New(sdkRootLogHandler{logger: logger})
}

func sdkLog(ctx context.Context, logger Logger, level slog.Level, message string, attrs ...LogField) {
	if contextual, ok := logger.(interface {
		TraceContext(context.Context, string, ...LogField)
		DebugContext(context.Context, string, ...LogField)
		InfoContext(context.Context, string, ...LogField)
		WarnContext(context.Context, string, ...LogField)
		ErrorContext(context.Context, string, error, ...LogField)
		FatalContext(context.Context, string, ...LogField)
	}); ok {
		switch {
		case level <= slog.Level(-8):
			contextual.TraceContext(ctx, message, attrs...)
		case level <= slog.LevelDebug:
			contextual.DebugContext(ctx, message, attrs...)
		case level <= slog.LevelInfo:
			contextual.InfoContext(ctx, message, attrs...)
		case level <= slog.LevelWarn:
			contextual.WarnContext(ctx, message, attrs...)
		case level <= slog.LevelError:
			contextual.ErrorContext(ctx, message, nil, attrs...)
		default:
			contextual.FatalContext(ctx, message, attrs...)
		}
		return
	}
	switch {
	case level <= slog.LevelDebug:
		if loggerWithDebug, ok := logger.(interface{ Debug(string, ...LogField) }); ok {
			loggerWithDebug.Debug(message, attrs...)
			return
		}
	case level <= slog.LevelInfo:
		if loggerWithInfo, ok := logger.(interface{ Info(string, ...LogField) }); ok {
			loggerWithInfo.Info(message, attrs...)
			return
		}
	case level <= slog.LevelWarn:
		logger.Warn(message, attrs...)
		return
	case level <= slog.LevelError:
		logger.Error(message, nil, attrs...)
		return
	default:
		if loggerWithFatal, ok := logger.(interface{ Fatal(string, ...LogField) }); ok {
			loggerWithFatal.Fatal(message, attrs...)
			return
		}
	}
	logger.Warn(message, attrs...)
}

func sdkLogError(ctx context.Context, logger Logger, level slog.Level, message string, err error, attrs ...LogField) {
	if level < slog.LevelError {
		sdkLog(ctx, logger, level, message, append(attrs, LogField{Key: "error", Value: err})...)
		return
	}
	if contextual, ok := logger.(interface {
		ErrorContext(context.Context, string, error, ...LogField)
	}); ok {
		if level >= slog.LevelError {
			contextual.ErrorContext(ctx, message, err, attrs...)
			return
		}
	}
	logger.Error(message, err, attrs...)
}

func slogValueAny(value slog.Value) any {
	if value.Kind() == slog.KindLogValuer {
		value = value.Resolve()
	}
	return value.Any()
}

func (s *Server) logSDKToolOutcome(ctx context.Context, name string, started time.Time, result *sdkmcp.CallToolResult, err error) {
	if s.logger == nil {
		return
	}
	outcome := sdkToolApplicationOutcome(ctx, result, err)
	attrs := []LogField{
		{Key: "tool", Value: name},
		{Key: "application_outcome", Value: outcome},
		{Key: "duration_ms", Value: time.Since(started).Milliseconds()},
	}
	if result != nil {
		attrs = appendSDKApplicationRefs(attrs, result.StructuredContent)
	}
	logCtx := ctx
	if ctx != nil {
		logCtx = context.WithoutCancel(ctx)
	}
	if outcome == "success" {
		if contextual, ok := s.logger.(interface {
			InfoContext(context.Context, string, ...LogField)
		}); ok {
			contextual.InfoContext(logCtx, "mcp_tool_outcome", attrs...)
		} else if loggerWithInfo, ok := s.logger.(interface{ Info(string, ...LogField) }); ok {
			loggerWithInfo.Info("mcp_tool_outcome", attrs...)
		}
		return
	}
	if contextual, ok := s.logger.(interface {
		ErrorContext(context.Context, string, error, ...LogField)
	}); ok {
		if err == nil {
			err = errors.New(outcome)
		}
		contextual.ErrorContext(logCtx, "mcp_tool_outcome", err, attrs...)
		return
	}
	s.logger.Error("mcp_tool_outcome", err, attrs...)
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

func appendSDKApplicationRefs(attrs []LogField, value any) []LogField {
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
			attrs = append(attrs, LogField{Key: key, Value: text})
		}
	}
	return attrs
}

// NewSDKHTTPHandler creates a stateless official-SDK transport backed by the
// request-scoped registry, authorization, and prompt catalog.
func (s *Server) NewSDKHTTPHandler(jsonResponse bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		annotateSDKToolDispatch(req)
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
			MaxRequestBodyBytes:          4 << 20,
			PropagateRequestCancellation: true,
		})
		transport.ServeHTTP(w, req)
	})
}

type sdkToolDispatchContextKey struct{}

// annotateSDKToolDispatch records whether a syntactically valid tools/call
// carries a JSON-RPC request id. The SDK invokes handlers for notifications as
// well, but notifications are not counted as dispatched tool calls.
func annotateSDKToolDispatch(req *http.Request) {
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return
	}
	if strings.ToLower(strings.TrimSpace(strings.SplitN(req.Header.Get("Content-Type"), ";", 2)[0])) != "application/json" {
		return
	}
	payload, err := io.ReadAll(io.LimitReader(req.Body, 4<<20+1))
	if err != nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	if len(payload) > 4<<20 {
		return
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.Method != "tools/call" {
		return
	}
	isRequest := len(envelope.ID) > 0 && sdkRPCIDValid(envelope.ID)
	ctx := context.WithValue(req.Context(), sdkToolDispatchContextKey{}, isRequest)
	*req = *req.WithContext(ctx)
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
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return false
	}
	if strings.ToLower(strings.TrimSpace(strings.SplitN(req.Header.Get("Content-Type"), ";", 2)[0])) != "application/json" {
		return false
	}
	if !sdkAcceptsStreamableHTTP(req.Header.Get("Accept")) {
		return false
	}
	payload, err := io.ReadAll(io.LimitReader(req.Body, 4<<20+1))
	if err != nil {
		return false
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	if len(payload) > 4<<20 {
		return false
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.Method != "tools/call" || strings.TrimSpace(envelope.Params.Name) == "" {
		return false
	}
	if !sdkStandardHeadersMatch(req, envelope.Method, envelope.Params.Name) {
		return false
	}
	if len(envelope.Params.Arguments) > 0 {
		var arguments map[string]any
		if json.Unmarshal(envelope.Params.Arguments, &arguments) != nil || arguments == nil {
			return false
		}
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
	tool, ok := s.registry.Get(envelope.Params.Name)
	policy := registry.ResolveRuntimeToolPolicy(req.Context(), s.runtimeToolPolicy, tool)
	visible := ok && registry.ToolVisible(req.Context(), tool, policy)
	if visible && s.canUseTool(tool) {
		return false
	}
	code, message := errCodeMethodNotFound, "tool not found: "+boundedRPCText(envelope.Params.Name)
	data := registry.ActionableInvalidInputData(req.Context(), envelope.Params.Name, "tool_not_available", "The requested tool is not available for this connection.", "Refresh tools/list and call an available tool.")
	scopeDenied := visible && !s.canUseTool(tool)
	if scopeDenied {
		code, message = errCodeToolFailure, "insufficient scope for tool"
		data = registry.ActionableAuthorizationData(req.Context(), envelope.Params.Name)
	}
	domain.RecordMCPToolCall(req.Context())
	domain.RecordMCPToolFailure(req.Context())
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
	attrs := []LogField{
		{Key: "application_outcome", Value: "tool_error"},
		{Key: "error_code", Value: code},
		{Key: "lookup_reason", Value: reason},
	}
	if visible && strings.TrimSpace(tool.Name) != "" {
		attrs = append(attrs, LogField{Key: "tool", Value: tool.Name})
	}
	logCtx := ctx
	if ctx != nil {
		logCtx = context.WithoutCancel(ctx)
	}
	if contextual, ok := s.logger.(interface {
		ErrorContext(context.Context, string, error, ...LogField)
	}); ok {
		contextual.ErrorContext(logCtx, "mcp_tool_outcome", errors.New("mcp tool lookup rejected"), attrs...)
		return
	}
	s.logger.Error("mcp_tool_outcome", errors.New("mcp tool lookup rejected"), attrs...)
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
		Logger:       newSDKRootLogger(s.logger),
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
				s.logSDKToolOutcome(ctx, name, started, result, err)
			}
		}()
		if sdkToolDispatchRequested(ctx) {
			domain.RecordMCPToolCall(ctx)
			defer func() {
				if err != nil || result == nil || result.IsError {
					domain.RecordMCPToolFailure(ctx)
				}
			}()
		}
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
	return registry.IsContractTool(tool) || registry.IsEvaluationTool(name)
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
