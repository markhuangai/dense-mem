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
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/promptcatalog"
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
	registry             registry.Registry
	profileID            string
	scopes               []string
	team                 TeamContext
	logger               observability.LogProvider
	recallFeedbackConfig registry.RecallFeedbackConfigProvider
	prompts              promptcatalog.Catalog
}

// TeamContext is non-secret team metadata made visible in MCP discovery so
// clients can distinguish multiple API-key-scoped Dense-Mem connections.
type TeamContext struct {
	Name        string
	Description string
}

// NewServer constructs a Server bound to a registry and a fixed profile ID.
func NewServer(reg registry.Registry, profileID string, logger observability.LogProvider) *Server {
	return NewServerWithScopesAndTeamContext(reg, profileID, nil, TeamContext{}, logger)
}

// NewServerWithScopes constructs a Server that filters visible/callable tools by scope.
func NewServerWithScopes(reg registry.Registry, profileID string, scopes []string, logger observability.LogProvider) *Server {
	return NewServerWithScopesAndTeamContext(reg, profileID, scopes, TeamContext{}, logger)
}

// NewServerWithScopesAndTeamContext constructs a Server with request-scoped team
// metadata for MCP discovery surfaces.
func NewServerWithScopesAndTeamContext(reg registry.Registry, profileID string, scopes []string, team TeamContext, logger observability.LogProvider) *Server {
	return NewServerWithScopesTeamContextAndRuntimeConfig(reg, profileID, scopes, team, logger, nil)
}

// NewServerWithScopesTeamContextAndRuntimeConfig constructs a Server with
// request-scoped team metadata and runtime feature visibility.
func NewServerWithScopesTeamContextAndRuntimeConfig(reg registry.Registry, profileID string, scopes []string, team TeamContext, logger observability.LogProvider, recallFeedbackConfig registry.RecallFeedbackConfigProvider) *Server {
	return &Server{
		registry:             reg,
		profileID:            profileID,
		scopes:               append([]string(nil), scopes...),
		team:                 normalizeTeamContext(team),
		logger:               logger,
		recallFeedbackConfig: recallFeedbackConfig,
		prompts:              defaultPromptCatalog(logger),
	}
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

// PayloadResult describes whether a JSON-RPC payload produced a response.
type PayloadResult struct {
	Payload []byte
	Respond bool
}

// HandlePayload handles one JSON-RPC request payload and returns one JSON-RPC response payload.
func (s *Server) HandlePayload(ctx context.Context, payload []byte) []byte {
	result := s.HandlePayloadResult(ctx, payload)
	return result.Payload
}

// HandlePayloadResult handles one JSON-RPC payload and reports whether the
// transport should write a response. Valid JSON-RPC notifications have no id and
// must not receive a JSON-RPC response.
func (s *Server) HandlePayloadResult(ctx context.Context, payload []byte) PayloadResult {
	var req rpcRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		s.logger.Warn("mcp: parse error", observability.String("error", err.Error()))
		return PayloadResult{Payload: mustMarshalResponse(errorResponse(nil, errCodeParseError, "parse error")), Respond: true}
	}
	if req.isNotification() {
		return PayloadResult{Respond: false}
	}
	return PayloadResult{Payload: mustMarshalResponse(s.dispatch(ctx, req)), Respond: true}
}

// dispatch routes a single request to the right handler.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return okResponse(req.ID, s.handleInitialize())
	case "tools/list":
		return okResponse(req.ID, s.handleToolsList(ctx))
	case "tools/call":
		result, rpcErr := s.handleToolsCall(ctx, req.Params)
		if rpcErr != nil {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		}
		return okResponse(req.ID, result)
	case "prompts/list":
		return okResponse(req.ID, s.handlePromptsList())
	case "prompts/get":
		result, rpcErr := s.handlePromptsGet(req.Params)
		if rpcErr != nil {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		}
		return okResponse(req.ID, result)
	default:
		s.logger.Warn("mcp: method not found", observability.String("method", req.Method))
		return errorResponse(req.ID, errCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (r rpcRequest) isNotification() bool {
	return len(r.ID) == 0 && r.JSONRPC == "2.0" && r.Method != ""
}

// handleInitialize returns the server's capability block.
func (s *Server) handleInitialize() map[string]any {
	serverInfo := map[string]any{
		"name":    s.serverName(),
		"version": ServerVersion,
	}
	if s.team.Name != "" {
		serverInfo["title"] = fmt.Sprintf("Dense-Mem: %s", s.team.Name)
	}
	if description := s.serverDescription(); description != "" {
		serverInfo["description"] = description
	}

	capabilities := map[string]any{
		"tools": map[string]any{},
	}
	if len(s.prompts.List()) > 0 {
		capabilities["prompts"] = map[string]any{}
	}

	out := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    capabilities,
		"serverInfo":      serverInfo,
	}
	if instructions := s.instructions(); instructions != "" {
		out["instructions"] = instructions
	}
	return out
}

func (s *Server) handlePromptsList() map[string]any {
	listed := s.prompts.List()
	out := make([]map[string]any, 0, len(listed))
	for _, prompt := range listed {
		args := make([]map[string]any, 0, len(prompt.Arguments))
		for _, arg := range prompt.Arguments {
			args = append(args, map[string]any{
				"name":        arg.Name,
				"description": arg.Description,
				"required":    arg.Required,
			})
		}
		item := map[string]any{
			"name":        prompt.Name,
			"description": prompt.Description,
			"arguments":   args,
		}
		if prompt.Title != "" {
			item["title"] = prompt.Title
		}
		out = append(out, item)
	}
	return map[string]any{"prompts": out}
}

type promptsGetParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handlePromptsGet(raw json.RawMessage) (map[string]any, *rpcError) {
	if len(raw) == 0 {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "missing params"}
	}
	var params promptsGetParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "invalid params"}
	}
	if params.Name == "" {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "missing prompt name"}
	}
	args := map[string]string{}
	for key, value := range params.Arguments {
		text, ok := value.(string)
		if !ok {
			return nil, &rpcError{
				Code:    errCodeInvalidParams,
				Message: fmt.Sprintf("argument %q must be a string", key),
			}
		}
		args[key] = text
	}
	prompt, text, err := s.prompts.Render(params.Name, args)
	if err != nil {
		if errors.Is(err, promptcatalog.ErrPromptNotFound) {
			return nil, &rpcError{Code: errCodeMethodNotFound, Message: err.Error()}
		}
		return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
	}
	return map[string]any{
		"description": prompt.Description,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": text,
				},
			},
		},
	}, nil
}

// handleToolsList returns registered tools mapped to MCP tool descriptors.
// The registry is already the source of truth so this is a pure transform.
func (s *Server) handleToolsList(ctx context.Context) map[string]any {
	listed := s.registry.List()
	out := make([]map[string]any, 0, len(listed))
	for _, t := range listed {
		if !registry.ToolVisible(ctx, t, s.recallFeedbackConfig) {
			continue
		}
		if !s.canUseTool(t) {
			continue
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": s.toolDescription(t.Description),
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
	if !registry.ToolVisible(ctx, tool, s.recallFeedbackConfig) {
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
	if registry.IsEvaluationTool(tool.Name) && registry.HasTenantOverrideArgs(args) {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "evaluation tools do not accept team_id or profile_id"}
	}
	if registry.IsV2ContractTool(tool) {
		if err := registry.ValidateV2ContractInput(tool, args, s.validationScopes(tool)); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
	} else {
		// Strip tenant IDs to prevent callers from overriding the fixed server
		// team. The HTTP API derives scope from the bearer key; local registries
		// may still receive a construction-time team for tests.
		registry.StripTenantOverrideArgs(args)
		args = registry.NormalizeInput(tool, args)
		if err := registry.ValidateInput(tool, args); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
	}
	result, err := tool.Invoke(ctx, s.profileID, args)
	if err != nil {
		if errors.Is(err, registry.ErrToolDisabled) {
			return nil, &rpcError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("tool not found: %s", params.Name)}
		}
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

func (s *Server) validationScopes(tool registry.Tool) []string {
	if s.scopes != nil {
		return s.scopes
	}
	return tool.RequiredScopes
}

func (s *Server) serverName() string {
	if s.team.Name == "" {
		return ServerName
	}
	slug := slugifyMCPName(s.team.Name)
	if slug == "" {
		return ServerName
	}
	return ServerName + "-" + slug
}

func (s *Server) serverDescription() string {
	if s.team.Name == "" && s.team.Description == "" {
		return ""
	}
	name := s.teamDisplayName()
	if s.team.Description == "" {
		return fmt.Sprintf("Dense-Mem MCP server for team %q.", name)
	}
	return fmt.Sprintf("Dense-Mem MCP server for team %q. Scope description: %s", name, s.team.Description)
}

func (s *Server) instructions() string {
	if s.team.Name == "" && s.team.Description == "" {
		return ""
	}
	name := s.teamDisplayName()
	parts := []string{
		fmt.Sprintf("Use this Dense-Mem MCP connection only for memories, evidence, claims, facts, recall, and knowledge operations that belong to the %q team.", name),
		"Use a personal memory MCP for unrelated personal identity, private-life facts, and cross-project preferences.",
		"If a memory could belong to both this team and a personal memory, ask before writing.",
	}
	if s.team.Description != "" {
		parts = append(parts, "Team scope description: "+s.team.Description)
	}
	return strings.Join(parts, " ")
}

func (s *Server) toolDescription(description string) string {
	if s.team.Name == "" && s.team.Description == "" {
		return description
	}

	prefixParts := []string{
		fmt.Sprintf("Dense-Mem team scope: %s.", s.teamDisplayName()),
		"Use this MCP only for memory and knowledge belonging to this team; use personal memory MCPs for unrelated personal facts.",
	}
	if s.team.Description != "" {
		prefixParts = append(prefixParts, "Scope description: "+s.team.Description)
	}
	prefix := strings.Join(prefixParts, " ")
	if strings.TrimSpace(description) == "" {
		return prefix
	}
	return prefix + " " + description
}

func (s *Server) teamDisplayName() string {
	if s.team.Name != "" {
		return s.team.Name
	}
	return "this team"
}

func normalizeTeamContext(team TeamContext) TeamContext {
	return TeamContext{
		Name:        normalizeMCPText(team.Name),
		Description: normalizeMCPText(team.Description),
	}
}

func normalizeMCPText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func slugifyMCPName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastHyphen := false
	for _, r := range value {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlnum {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultPromptCatalog(logger observability.LogProvider) promptcatalog.Catalog {
	return promptCatalogOrEmpty(logger, promptcatalog.Default)
}

func promptCatalogOrEmpty(logger observability.LogProvider, load func() (promptcatalog.Catalog, error)) promptcatalog.Catalog {
	catalog, err := load()
	if err != nil {
		if logger != nil {
			logger.Warn("mcp: prompt catalog unavailable", observability.String("error", err.Error()))
		}
		return promptcatalog.Catalog{}
	}
	return catalog
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
