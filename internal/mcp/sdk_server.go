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

// NewSDKHTTPHandler creates a stateless official-SDK transport backed by the
// same request-scoped registry, authorization, and prompt catalog as the
// legacy transport.
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
	payload, err := io.ReadAll(io.LimitReader(req.Body, 4<<20+1))
	if err != nil || len(payload) > 4<<20 {
		return false
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.Method != "tools/call" || strings.TrimSpace(envelope.Params.Name) == "" {
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
		Logger:       slog.Default(),
	})

	for _, tool := range s.registry.List() {
		policy := registry.ResolveRuntimeToolPolicy(ctx, s.runtimeToolPolicy, tool)
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
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}, nil
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
