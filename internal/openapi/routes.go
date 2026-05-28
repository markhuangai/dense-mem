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
		// --- Claims (AI-safe, knowledge pipeline Phase 2 & 3) ---
		{Method: "POST", Path: "/api/v1/claims", OperationID: "createClaim", RequestSchema: "ClaimRequest", ResponseSchema: "ClaimResponse", SuccessStatus: 201, AISafe: true, Tags: []string{"knowledge"}, Description: "Create a new candidate claim derived from a source fragment."},
		{Method: "GET", Path: "/api/v1/claims/{id}", OperationID: "getClaim", ResponseSchema: "ClaimResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Fetch a single claim by id."},
		{Method: "GET", Path: "/api/v1/claims", OperationID: "listClaims", AISafe: true, Tags: []string{"knowledge"}, Description: "List claims (keyset pagination)."},
		{Method: "DELETE", Path: "/api/v1/claims/{id}", OperationID: "deleteClaim", AISafe: true, Tags: []string{"knowledge"}, Description: "Hard-delete a claim."},
		// Phase 3: entailment verification (AC-28, AC-30)
		{Method: "POST", Path: "/api/v1/claims/{id}/verify", OperationID: "verifyClaim", ResponseSchema: "VerifyClaimResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Run entailment verification for a candidate claim.", ExtraResponses: map[string]string{
			"429": "Verifier rate-limited; retry after Retry-After seconds.",
			"502": "Verifier returned a malformed response.",
			"503": "Verifier provider unavailable.",
			"504": "Verifier request timed out.",
		}},
		// Phase 4: fact promotion (AC-41, AC-42)
		{Method: "POST", Path: "/api/v1/claims/{id}/promote", OperationID: "promoteClaim", ResponseSchema: "FactResponse", SuccessStatus: 201, AISafe: true, Tags: []string{"knowledge"}, Description: "Promote a validated claim to an authoritative fact.", ExtraResponses: map[string]string{
			"409": "Claim not validated, gate rejected, claim marked disputed due to a comparable existing fact, or claim weaker than existing fact.",
			"422": "Predicate not policed or unsupported promotion policy.",
		}},

		// --- Facts (AI-safe, knowledge pipeline Phase 4) ---
		{Method: "GET", Path: "/api/v1/facts/{id}", OperationID: "getFact", ResponseSchema: "FactResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Fetch a single promoted fact by id."},
		{Method: "GET", Path: "/api/v1/facts", OperationID: "listFacts", AISafe: true, Tags: []string{"knowledge"}, Description: "List promoted facts (keyset pagination)."},
		{Method: "GET", Path: "/api/v1/communities", OperationID: "listCommunities", ResponseSchema: "ListCommunitiesResponse", AISafe: true, Tags: []string{"community"}, Description: "List persisted community summaries for the caller's profile."},
		{Method: "GET", Path: "/api/v1/communities/{id}", OperationID: "getCommunitySummary", ResponseSchema: "CommunityResponse", AISafe: true, Tags: []string{"community"}, Description: "Fetch one persisted community summary by community_id."},

		// --- Fragments (AI-safe) ---
		{Method: "POST", Path: "/api/v1/fragments", OperationID: "createFragment", ToolName: "save_memory", AISafe: true, Description: "Save a new memory fragment."},
		{Method: "GET", Path: "/api/v1/fragments", OperationID: "listFragments", ToolName: "list_recent_memories", AISafe: true, Description: "List recent fragments (keyset pagination)."},
		{Method: "GET", Path: "/api/v1/fragments/{id}", OperationID: "getFragment", ToolName: "get_memory", AISafe: true, Description: "Fetch a single fragment by id."},
		{Method: "DELETE", Path: "/api/v1/fragments/{id}", OperationID: "deleteFragment", AISafe: true, Description: "Hard-delete a fragment."},
		// Phase 6: soft tombstone (AC-48)
		{Method: "POST", Path: "/api/v1/fragments/{id}/retract", OperationID: "retractFragment", ResponseSchema: "RetractFragmentResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Soft-tombstone a fragment; preserves graph lineage and triggers fact revalidation."},

		// --- Tool catalog (AI-safe) ---
		{Method: "GET", Path: "/api/v1/tools", OperationID: "listTools", ResponseSchema: "ToolCatalogResponse", AISafe: true, Description: "List all registered tools."},
		{Method: "POST", Path: "/api/v1/tools/{name}", OperationID: "executeTool", RequestSchema: "ToolExecuteRequest", ResponseSchema: "ToolExecuteResponse", Description: "Execute a registered tool by name. Discover per-tool schemas and scope requirements via GET /api/v1/tools first."},

		// --- Recall (AI-safe) ---
		{Method: "GET", Path: "/api/v1/recall", OperationID: "recallKnowledge", ResponseSchema: "RecallResponse", AISafe: true, Tags: []string{"knowledge"}, Description: "Hybrid semantic + keyword recall spanning all knowledge-pipeline tiers (facts, claims, fragments).", ExtraResponses: map[string]string{
			"400": "Missing or invalid query parameter.",
			"503": "Embedding provider unavailable.",
		}},
		{Method: "POST", Path: "/api/v1/tools/recall-memory", OperationID: "recallMemory", ToolName: "recall_memory", AISafe: true, Description: "Hybrid semantic + keyword recall over fragments."},

		// --- Advanced tool routes (full variant only) ---
		{Method: "POST", Path: "/api/v1/tools/graph-query", OperationID: "graphQueryTool", ToolName: "graph-query", Description: "Advanced: read-only Cypher query."},
		{Method: "POST", Path: "/api/v1/tools/keyword-search", OperationID: "keywordSearchTool", ToolName: "keyword-search", Description: "Advanced: BM25 keyword search."},
		{Method: "POST", Path: "/api/v1/tools/semantic-search", OperationID: "semanticSearchTool", ToolName: "semantic-search", Description: "Advanced: kNN vector search."},

		// --- MCP Streamable HTTP (full runtime variant) ---
		{Method: "POST", Path: "/mcp", OperationID: "mcpPost", Description: "MCP Streamable HTTP JSON-RPC endpoint."},
		{Method: "GET", Path: "/mcp", OperationID: "mcpGet", Description: "MCP Streamable HTTP SSE stream endpoint."},

		// --- Team routes (full runtime variant) ---
		{Method: "GET", Path: "/api/v1/teams/{teamId}", OperationID: "getTeam", Description: "Get a team."},
		{Method: "PATCH", Path: "/api/v1/teams/{teamId}", OperationID: "patchTeam", Description: "Update team metadata."},
		{Method: "GET", Path: "/api/v1/teams/{teamId}/profiles", OperationID: "listTeamProfiles", Description: "List named profiles and key metadata for a team."},
		{Method: "POST", Path: "/api/v1/teams/{teamId}/profiles", OperationID: "createTeamProfile", Description: "Create a named profile and return its API key once."},
		{Method: "GET", Path: "/api/v1/teams/{teamId}/profiles/{profileId}", OperationID: "getTeamProfile", Description: "Get one named profile and key metadata."},
		{Method: "POST", Path: "/api/v1/teams/{teamId}/profiles/{profileId}/rotate", OperationID: "rotateTeamProfileKey", Description: "Rotate a named profile's API key in place."},
		{Method: "DELETE", Path: "/api/v1/teams/{teamId}/profiles/{profileId}", OperationID: "deleteTeamProfile", Description: "Delete a named profile and its API key."},

		// --- Audit log (full variant) ---
		{Method: "GET", Path: "/api/v1/teams/{teamId}/audit-log", OperationID: "getAuditLog", Description: "Fetch the team's audit log."},

		// --- SSE query stream (full variant) ---
		{Method: "POST", Path: "/api/v1/teams/{teamId}/query/stream", OperationID: "queryStream", Description: "Server-sent event stream for long-running queries."},
	}
}
