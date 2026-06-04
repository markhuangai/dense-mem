// Package mcp implements a minimal Model Context Protocol (MCP) server over
// JSON-RPC 2.0 for the Streamable HTTP transport.
//
// Design constraints (Unit 24 plan):
//   - Tool discovery and execution are fully delegated to the shared
//     registry.Registry — no business logic lives here. (AC-37)
//   - The HTTP API derives the team from the API key. Callers cannot switch
//     teams by injecting profile_id or team_id into tool arguments.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/tools"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// ProtocolVersion is the MCP protocol revision this server speaks.
const ProtocolVersion = "2024-11-05"

// ServerName and ServerVersion are surfaced in the initialize response.
const (
	ServerName    = "dense-mem"
	ServerVersion = "0.1.0"
)

// JSON-RPC 2.0 error codes used by the server.
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeToolFailure    = -32000
)

// Server is an MCP server bound to a shared tool registry.
type Server struct {
	registry  registry.Registry
	profileID string
	scopes    []string
	logger    observability.LogProvider
}

// NewServer constructs a Server bound to a registry and a fixed profile ID.
func NewServer(reg registry.Registry, profileID string, logger observability.LogProvider) *Server {
	return &Server{registry: reg, profileID: profileID, logger: logger}
}

// NewServerWithScopes constructs a Server that filters visible/callable tools by scope.
func NewServerWithScopes(reg registry.Registry, profileID string, scopes []string, logger observability.LogProvider) *Server {
	return &Server{registry: reg, profileID: profileID, scopes: append([]string(nil), scopes...), logger: logger}
}

// rpcRequest mirrors the incoming JSON-RPC 2.0 envelope.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse mirrors the outgoing JSON-RPC 2.0 envelope.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// HandlePayload handles one JSON-RPC request payload and returns one JSON-RPC response payload.
func (s *Server) HandlePayload(ctx context.Context, payload []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		s.logger.Warn("mcp: parse error", observability.String("error", err.Error()))
		return mustMarshalResponse(errorResponse(nil, errCodeParseError, "parse error"))
	}
	return mustMarshalResponse(s.dispatch(ctx, req))
}

// dispatch routes a single request to the right handler.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return okResponse(req.ID, s.handleInitialize())
	case "tools/list":
		return okResponse(req.ID, s.handleToolsList())
	case "tools/call":
		result, rpcErr := s.handleToolsCall(ctx, req.Params)
		if rpcErr != nil {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		}
		return okResponse(req.ID, result)
	default:
		s.logger.Warn("mcp: method not found", observability.String("method", req.Method))
		return errorResponse(req.ID, errCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleInitialize returns the server's capability block.
func (s *Server) handleInitialize() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ServerVersion,
		},
	}
}

// handleToolsList returns registered tools mapped to MCP tool descriptors.
// The registry is already the source of truth so this is a pure transform.
func (s *Server) handleToolsList() map[string]any {
	listed := s.registry.List()
	out := make([]map[string]any, 0, len(listed))
	for _, t := range listed {
		if !s.canUseTool(t) {
			continue
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return map[string]any{"tools": out}
}

// toolsCallParams is the MCP tools/call payload.
type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// handleToolsCall delegates to registry.Get(name).Invoke — no business logic
// lives in this package. Errors are sanitized so provider credentials or raw
// internal messages do not leak to the MCP client.
func (s *Server) handleToolsCall(ctx context.Context, raw json.RawMessage) (map[string]any, *rpcError) {
	if len(raw) == 0 {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "missing params"}
	}
	var params toolsCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "invalid params"}
	}
	if params.Name == "" {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "missing tool name"}
	}
	tool, ok := s.registry.Get(params.Name)
	if !ok {
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("tool not found: %s", params.Name)}
	}
	if !s.canUseTool(tool) {
		return nil, &rpcError{Code: errCodeToolFailure, Message: "insufficient scope for tool"}
	}
	if tool.Invoke == nil {
		return nil, &rpcError{Code: errCodeToolFailure, Message: "tool not executable"}
	}

	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	// Strip tenant IDs to prevent callers from overriding the fixed server
	// team. The HTTP API derives scope from the bearer key; local registries
	// may still receive a construction-time team for tests.
	registry.StripTenantOverrideArgs(args)
	if err := registry.ValidateInput(tool, args); err != nil {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
	}
	result, err := tool.Invoke(ctx, s.profileID, args)
	if err != nil {
		s.logger.Error("mcp: tool invocation failed", err,
			observability.String("tool", params.Name),
			observability.ProfileID(s.profileID),
		)
		return nil, &rpcError{Code: errCodeToolFailure, Message: tools.SanitizeError(err)}
	}

	payload, err := json.Marshal(result)
	if err != nil {
		s.logger.Error("mcp: tool result marshal failed", err, observability.String("tool", params.Name))
		return nil, &rpcError{Code: errCodeToolFailure, Message: "tool result serialization failed"}
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(payload)},
		},
	}, nil
}

func (s *Server) canUseTool(tool registry.Tool) bool {
	if s.scopes == nil {
		return true
	}
	if len(tool.RequiredScopes) == 0 {
		return true
	}
	scopeSet := make(map[string]struct{}, len(s.scopes))
	for _, scope := range s.scopes {
		scopeSet[scope] = struct{}{}
	}
	for _, required := range tool.RequiredScopes {
		if _, ok := scopeSet[required]; !ok {
			return false
		}
	}
	return true
}

func okResponse(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func mustMarshalResponse(resp rpcResponse) []byte {
	payload, err := json.Marshal(resp)
	if err != nil {
		fallback := errorResponse(resp.ID, errCodeToolFailure, "response serialization failed")
		payload, _ = json.Marshal(fallback)
	}
	return payload
}
