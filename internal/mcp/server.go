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

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/promptcatalog"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// ProtocolVersion is the MCP protocol revision this server speaks.
const ProtocolVersion = "2025-11-25"

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
	registry          registry.Registry
	teamID            string
	scopes            []string
	team              TeamContext
	logger            observability.LogProvider
	runtimeToolPolicy registry.RuntimeToolPolicy
	prompts           promptcatalog.Catalog
}

// TeamContext is non-secret team metadata made visible in MCP discovery so
// clients can distinguish multiple API-key-scoped Dense-Mem connections.
type TeamContext struct {
	Name        string
	Description string
}

// NewServer constructs a Server bound to a registry and a fixed team ID.
func NewServer(reg registry.Registry, teamID string, logger observability.LogProvider) *Server {
	return NewServerWithScopesAndTeamContext(reg, teamID, nil, TeamContext{}, logger)
}

// NewServerWithScopes constructs a Server that filters visible/callable tools by scope.
func NewServerWithScopes(reg registry.Registry, teamID string, scopes []string, logger observability.LogProvider) *Server {
	return NewServerWithScopesAndTeamContext(reg, teamID, scopes, TeamContext{}, logger)
}

// NewServerWithScopesAndTeamContext constructs a Server with request-scoped team
// metadata for MCP discovery surfaces.
func NewServerWithScopesAndTeamContext(reg registry.Registry, teamID string, scopes []string, team TeamContext, logger observability.LogProvider) *Server {
	return NewServerWithScopesTeamContextAndRuntimeConfig(reg, teamID, scopes, team, logger, nil)
}

// NewServerWithScopesTeamContextAndRuntimeConfig constructs a Server with
// request-scoped team metadata and runtime feature visibility.
func NewServerWithScopesTeamContextAndRuntimeConfig(reg registry.Registry, teamID string, scopes []string, team TeamContext, logger observability.LogProvider, recallFeedbackConfig registry.RecallFeedbackConfigProvider, dreams ...registry.DreamingConfigProvider) *Server {
	var dreamConfig registry.DreamingConfigProvider
	if len(dreams) > 0 {
		dreamConfig = dreams[0]
	}
	return &Server{
		registry: reg,
		teamID:   teamID,
		scopes:   append([]string(nil), scopes...),
		team:     normalizeTeamContext(team),
		logger:   logger,
		runtimeToolPolicy: registry.RuntimeToolPolicy{
			RecallFeedback: recallFeedbackConfig,
			Dreams:         dreamConfig,
		},
		prompts: defaultPromptCatalog(logger),
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// invokeTool is the registry-only execution seam used by the official SDK.
func (s *Server) invokeTool(ctx context.Context, name string, args map[string]any) (map[string]any, *rpcError) {
	if name == "" {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "missing tool name"}
	}
	tool, ok := s.registry.Get(name)
	if !ok {
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: "tool not found: " + boundedRPCText(name)}
	}
	policy := registry.ResolveRuntimeToolPolicy(ctx, s.runtimeToolPolicy, tool)
	if !registry.ToolVisible(ctx, tool, policy) {
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: "tool not found: " + boundedRPCText(name)}
	}
	if !s.canUseTool(tool) {
		return nil, &rpcError{Code: errCodeToolFailure, Message: "insufficient scope for tool"}
	}
	if tool.Invoke == nil {
		return nil, &rpcError{Code: errCodeToolFailure, Message: "tool not executable"}
	}

	if args == nil {
		args = map[string]any{}
	}
	if registry.IsEvaluationTool(tool.Name) && registry.HasTenantOverrideArgs(args) {
		s.logToolInputRejected(ctx, tool.Name, "tenant_override_rejected")
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "evaluation tools do not accept team_id or profile_id"}
	}
	if registry.IsContractTool(tool) {
		if err := registry.ValidateContractInput(tool, args, s.validationScopes(tool)); err != nil {
			reasonCode := "contract_validation_failed"
			if registry.HasTenantOverrideArgs(args) {
				reasonCode = "tenant_override_rejected"
			}
			s.logToolInputRejected(ctx, tool.Name, reasonCode)
			return nil, &rpcError{Code: errCodeInvalidParams, Message: boundedRPCText(err.Error())}
		}
	} else {
		// Strip tenant IDs to prevent callers from overriding the fixed server
		// team. The HTTP API derives scope from the bearer key; local registries
		// may still receive a construction-time team for tests.
		registry.StripTenantOverrideArgs(args)
		if err := registry.ValidateInput(tool, args); err != nil {
			s.logToolInputRejected(ctx, tool.Name, "input_validation_failed")
			return nil, &rpcError{Code: errCodeInvalidParams, Message: boundedRPCText(err.Error())}
		}
	}
	ctx = registry.WithRuntimeToolPolicy(ctx, policy)
	result, err := tool.Invoke(ctx, s.teamID, args)
	if err != nil {
		if errors.Is(err, registry.ErrToolDisabled) {
			return nil, &rpcError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("tool not found: %s", name)}
		}
		s.logger.Error("mcp: tool invocation failed", err,
			observability.String("tool", name),
			observability.String("team_id", s.teamID),
		)
		return nil, &rpcError{Code: errCodeToolFailure, Message: boundedRPCText(tools.SanitizeError(err))}
	}

	payload, err := json.Marshal(result)
	if err != nil {
		s.logger.Error("mcp: tool result marshal failed", err, observability.String("tool", name))
		return nil, &rpcError{Code: errCodeToolFailure, Message: "tool result serialization failed"}
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(payload)},
		},
	}, nil
}

func (s *Server) logToolInputRejected(ctx context.Context, toolName, reasonCode string) {
	if s.logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("tool", toolName),
		observability.String("team_id", s.teamID),
		observability.String("reference_type", "mcp_tool"),
		observability.String("reference_id", toolName),
		observability.String("reason_code", reasonCode),
	}
	if correlationID := correlation.FromContext(ctx); correlationID != "" {
		attrs = append(attrs, observability.CorrelationID(correlationID))
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.OwnerID != uuid.Nil {
		attrs = append(attrs, observability.ProfileID(actor.OwnerID.String()))
	}
	s.logger.Warn("mcp_tool_input_rejected", attrs...)
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

func boundedRPCText(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= 512 {
		return value
	}
	return string([]rune(value)[:512])
}
