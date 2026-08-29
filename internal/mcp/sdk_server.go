package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

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

// NewSDKHTTPHandler creates a stateless official-SDK transport backed by the
// request-scoped registry, authorization, and prompt catalog.
func (s *Server) NewSDKHTTPHandler(jsonResponse bool) http.Handler {
	transport := sdkmcp.NewStreamableHTTPHandler(func(req *http.Request) *sdkmcp.Server {
		return s.newSDKServer(req.Context())
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 jsonResponse,
		MaxRequestBodyBytes:          4 << 20,
		PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if s.writeSDKToolLookupError(w, req, jsonResponse) {
			return
		}
		transport.ServeHTTP(w, req)
	})
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
	if visible && !s.canUseTool(tool) {
		code, message = errCodeToolFailure, "insufficient scope for tool"
	}
	response := map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": code, "message": message}}
	if len(envelope.ID) > 0 {
		var id any
		if json.Unmarshal(envelope.ID, &id) == nil {
			response["id"] = id
		}
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
		Logger:       newSDKLogger(slog.Default()),
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
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var args map[string]any
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, &sdkjsonrpc.Error{Code: errCodeInvalidParams, Message: "invalid params"}
			}
		}
		result, rpcErr := s.invokeTool(ctx, name, args)
		if rpcErr != nil {
			return nil, sdkRPCError(rpcErr)
		}
		content, ok := result["content"].([]map[string]any)
		if !ok || len(content) != 1 {
			return nil, &sdkjsonrpc.Error{Code: errCodeToolFailure, Message: "tool result serialization failed"}
		}
		text, ok := content[0]["text"].(string)
		if !ok {
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
