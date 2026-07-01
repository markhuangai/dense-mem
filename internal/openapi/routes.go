// Package openapi generates OpenAPI 3.0.3 specifications from the tool
// registry and a declarative route table. The spec is produced at runtime so
// it always reflects the live set of tools and routes; it is never hand
// edited (AC-34, AC-35).
package openapi

// RouteDescriptor is the static metadata for one HTTP route. The generator
// classifies routes by the AISafe flag. Routes backed by a
// registered tool name inherit their request/response schemas from the
// registry rather than duplicating them here.
type RouteDescriptor struct {
	Method      string
	Path        string
	OperationID string
	// ToolName links to a registry entry. When set, the operation's request
	// body schema and 200-response schema are pulled directly from the
	// registry tool's InputSchema / OutputSchema (AC-34 "derived from
	// registry"). Empty for routes with no tool counterpart.
	ToolName    string
	AISafe      bool
	Description string

	// RequestSchema is an explicit component-schema name for the request body.
	// When set, it takes priority over any ToolName-derived input schema.
	// This allows routes that ship before MCP tool registration to declare
	// their schemas directly.
	RequestSchema string

	// ResponseSchema is an explicit component-schema name for the success
	// response body. When set, it takes priority over any ToolName-derived
	// output schema.
	ResponseSchema string

	// SuccessStatus is the HTTP status code for the success response. Defaults
	// to 200 when zero. Use 201 for resource-creation routes.
	SuccessStatus int

	// ExtraResponses maps additional HTTP status codes to their descriptions.
	// These are merged into the operation's responses block alongside the
	// standard error codes.
	ExtraResponses map[string]string

	// Tags overrides the auto-inferred operation tags when non-empty. If
	// empty, tags are derived from the route path and flags as before.
	Tags []string
}

// DefaultRoutes returns the canonical route table for the v1 surface. New
// routes registered in router_protected.go must also be added here so they
// surface in the generated OpenAPI doc.
func DefaultRoutes() []RouteDescriptor {
	return []RouteDescriptor{
		// --- Tool catalog (AI-safe) ---
		{Method: "GET", Path: "/api/v1/tools", OperationID: "listTools", ResponseSchema: "ToolCatalogResponse", AISafe: true, Description: "List all registered tools."},
		{Method: "POST", Path: "/api/v1/tools/{name}", OperationID: "executeTool", RequestSchema: "ToolExecuteRequest", ResponseSchema: "ToolExecuteResponse", Description: "Execute a registered tool by name. Discover per-tool schemas and scope requirements via GET /api/v1/tools first."},

		// --- Recall (AI-safe) ---
		{Method: "GET", Path: "/api/v1/recall", OperationID: "recallKnowledge", ResponseSchema: "RecallResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Hybrid semantic + keyword recall spanning all knowledge-pipeline tiers (facts, claims, fragments).", ExtraResponses: map[string]string{
			"400": "Missing or invalid query parameter.",
			"503": "Embedding provider unavailable.",
		}},
		{Method: "POST", Path: "/api/v1/tools/recall_memory", OperationID: "recallMemory", ToolName: "recall_memory", AISafe: true, Description: "Hybrid semantic + keyword recall over fragments."},
		{Method: "POST", Path: "/api/v1/tools/remember", OperationID: "rememberTool", ToolName: "remember", AISafe: true, Description: "Submit evidence for server-owned memory placement."},
		{Method: "POST", Path: "/api/v1/tools/get_memory_placement", OperationID: "getMemoryPlacementTool", ToolName: "get_memory_placement", AISafe: true, Description: "Poll a server-owned memory placement run."},
		{Method: "POST", Path: "/api/v1/tools/dispute_memory_placement", OperationID: "disputeMemoryPlacementTool", ToolName: "dispute_memory_placement", AISafe: true, Description: "Dispute a memory placement with more evidence."},
		{Method: "POST", Path: "/api/v1/tools/import_memories", OperationID: "importMemoriesTool", ToolName: "import_memories", AISafe: true, Description: "Trusted historical import path that may include explicit claims."},
		{Method: "POST", Path: "/api/v1/tools/reflect_memories", OperationID: "reflectMemoriesTool", ToolName: "reflect_memories", AISafe: true, Description: "Review memory health and unresolved clarification needs."},
		{Method: "POST", Path: "/api/v1/tools/confirm_memory", OperationID: "confirmMemoryTool", ToolName: "confirm_memory", AISafe: true, Description: "Apply an explicit memory clarification decision."},
		{Method: "POST", Path: "/api/v1/tools/list_dreams", OperationID: "listDreamsTool", ToolName: "list_dreams", AISafe: true, Description: "List reviewable dream hypotheses."},
		{Method: "POST", Path: "/api/v1/tools/get_dream", OperationID: "getDreamTool", ToolName: "get_dream", AISafe: true, Description: "Fetch one dream hypothesis and its source references."},
		{Method: "POST", Path: "/api/v1/tools/resolve_dream_feedback", OperationID: "resolveDreamFeedbackTool", ToolName: "resolve_dream_feedback", AISafe: true, Description: "Apply evidence-driven feedback to a dream hypothesis and route confirmed true or false resolutions through memory placement."},

		// --- MCP Streamable HTTP (full runtime variant) ---
		{Method: "POST", Path: "/mcp", OperationID: "mcpPost", Description: "MCP Streamable HTTP JSON-RPC endpoint."},
		{Method: "GET", Path: "/mcp", OperationID: "mcpGet", Description: "MCP Streamable HTTP SSE stream endpoint."},

		// --- Team routes (full runtime variant) ---
		{Method: "GET", Path: "/api/v1/teams/{teamId}", OperationID: "getTeam", Description: "Get a team."},
		{Method: "PATCH", Path: "/api/v1/teams/{teamId}", OperationID: "patchTeam", Description: "Update team metadata. Requires a manager key for the team."},
		{Method: "GET", Path: "/api/v1/teams/{teamId}/profiles", OperationID: "listTeamProfiles", Description: "List named profiles and key metadata for a team. Requires a manager key for the team."},
		{Method: "POST", Path: "/api/v1/teams/{teamId}/profiles", OperationID: "createTeamProfile", Description: "Create a member profile and return its API key once. Requires a manager key for the team."},
		{Method: "GET", Path: "/api/v1/teams/{teamId}/profiles/{profileId}", OperationID: "getTeamProfile", Description: "Get one named profile and key metadata. Requires a manager key for the team."},
		{Method: "PATCH", Path: "/api/v1/teams/{teamId}/profiles/{profileId}", OperationID: "patchTeamProfile", Description: "Rename a member profile. Requires a manager key for the team."},
		{Method: "POST", Path: "/api/v1/teams/{teamId}/profiles/{profileId}/rotate", OperationID: "rotateTeamProfileKey", Description: "Rotate a member profile's API key in place. Requires a manager key for the team."},
		{Method: "DELETE", Path: "/api/v1/teams/{teamId}/profiles/{profileId}", OperationID: "deleteTeamProfile", Description: "Delete a member profile and its API key. Requires a manager key for the team."},

		// --- Audit log (full variant) ---
		{Method: "GET", Path: "/api/v1/teams/{teamId}/audit-log", OperationID: "getAuditLog", Description: "Fetch the team's audit log."},
	}
}
