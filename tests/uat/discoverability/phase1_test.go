//go:build uat

package discoverability

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// Phase-1 UATs pin each downstream unit deliverable to a static source artefact
// plus a minimal in-process exercise of the public type. The aim is coverage of
// every AC-to-unit trace without standing up containers — live-stack coverage
// lives in the outer tests/uat harness.

// UAT-5: Embedding provider startup, config, and consistency (Unit 6–10).
// AC trace: AC-11, AC-12, AC-13, AC-14, AC-15, AC-16, AC-47, AC-54.
func TestUAT5_EmbeddingProviderStartup(t *testing.T) {
	// Config contract must expose the four AI_* getters used by the bootstrap
	// and the IsEmbeddingConfigured helper used during server setup.
	cfg := readFile(t, "internal/config/config.go")
	for _, sym := range []string{
		"GetAIAPIURL", "GetAIAPIKey",
		"GetAIEmbeddingModel", "GetAIEmbeddingDimensions",
		"IsEmbeddingConfigured",
	} {
		assert.Contains(t, cfg, sym, "config.go must declare %q", sym)
	}

	// Provider interface + OpenAI implementation + retry + sanitize must all
	// exist as separate artefacts (Units 7–9).
	assert.Contains(t, readFile(t, "internal/embedding/provider.go"),
		"type EmbeddingProviderInterface interface",
		"EmbeddingProviderInterface is the public contract")
	assert.Contains(t, readFile(t, "internal/embedding/openai_provider.go"),
		"openai", "OpenAI-protocol provider must exist")
	assert.Contains(t, readFile(t, "internal/embedding/retry_provider.go"),
		"Embed", "retry wrapper must forward Embed")
	sanitize := readFile(t, "internal/embedding/sanitize.go")
	assert.Contains(t, sanitize, "SanitizeError",
		"embedding must expose SanitizeError — provider secrets must not leak")
	assert.Contains(t, sanitize, "REDACTED",
		"sanitizer must replace secrets with a redaction placeholder")

	// Embedding consistency service (Unit 10) must declare a dimensions gate.
	assert.Contains(t, readFile(t, "internal/service/embedding_consistency.go"),
		"dimension", "consistency service must reference dimensions")
}

// UAT-6: Fragment create remains internal behind the evidence-first memory API.
// AC trace: AC-17, AC-20, AC-23, AC-24, AC-25, AC-26, AC-49, AC-52, AC-54.
func TestUAT6_FragmentCreateInternalBehindRemember(t *testing.T) {
	create := readFile(t, "internal/service/fragmentservice/create.go")

	// The create service must persist all day-1 embedding metadata properties
	// — AC-12/AC-14 require these on every new fragment.
	for _, prop := range []string{
		"embedding_model", "embedding_dimensions",
		"content_hash", "idempotency_key",
	} {
		assert.Contains(t, create, prop, "create.go must persist %q", prop)
	}
	assert.Contains(t, create, "CorrelationID",
		"audit entry must carry CorrelationID (AC-54)")

	memory := readFile(t, "internal/service/memoryservice/memory.go")
	assert.Contains(t, memory, "FragmentCreate",
		"remember must keep fragment creation as an internal service dependency")

	router := readFile(t, "internal/http/router_protected.go")
	assert.NotContains(t, router, "FragmentCreate",
		"POST /fragments must not be mounted as a public client route")
}

// UAT-7: Fragment create validation rules live on the shared DTO (Unit 13).
// AC trace: AC-18, AC-19, AC-46, AC-54.
func TestUAT7_FragmentCreateValidationRules(t *testing.T) {
	body := readFile(t, "internal/http/dto/fragment.go")

	// Validator tags codify the size/enum contract that AC-18 and AC-19 pin.
	assert.Contains(t, body, "required,max=8192,notblank",
		"Content must be bounded to 8KB and non-blank")
	assert.Contains(t, body, "oneof=conversation document observation manual",
		"SourceType must be an enum matching domain.SourceType")
	assert.Contains(t, body, "MaxMetadataBytes",
		"DTO must expose MaxMetadataBytes so handlers can size-check metadata")

	// Public contract check — keep the struct stable for callers.
	var req dto.CreateFragmentRequest
	req.Content = "hello"
	req.SourceType = string(domain.SourceTypeConversation)
	req.IdempotencyKey = "k"
	req.Labels = []string{"a", "b"}
	assert.Equal(t, "hello", req.Content)
	assert.Equal(t, "conversation", req.SourceType)
	assert.Equal(t, "k", req.IdempotencyKey)
	assert.Len(t, req.Labels, 2)

	// ValidateMetadataSize must enforce the documented cap.
	oversize := map[string]any{"payload": strings.Repeat("x", dto.MaxMetadataBytes+1)}
	assert.Error(t, dto.ValidateMetadataSize(oversize),
		"metadata over MaxMetadataBytes must be rejected")
	assert.NoError(t, dto.ValidateMetadataSize(nil),
		"nil metadata is allowed")
}

// UAT-8: Dedupe by idempotency key and by content hash (Unit 14).
// AC trace: AC-21, AC-22, AC-24, AC-25, AC-44, AC-45, AC-50.
func TestUAT8_FragmentDeduplication(t *testing.T) {
	dedupe := readFile(t, "internal/service/fragmentdedupe/dedupe.go")
	for _, m := range []string{"ByIdempotencyKey", "ByContentHash"} {
		assert.Contains(t, dedupe, m,
			"dedupe package must expose %q", m)
	}
	// Must be scoped by profile_id — this is the whole point of profile isolation.
	assert.Contains(t, dedupe, "profile_id",
		"dedupe queries must filter by profile_id (cross-profile isolation)")

	create := readFile(t, "internal/service/fragmentservice/create.go")
	// Create flow must consult both lookups before issuing a write.
	assert.Contains(t, create, "ByIdempotencyKey",
		"create service must check idempotency key before embedding")
	assert.Contains(t, create, "ByContentHash",
		"create service must check content hash before embedding")
	// Duplicates must short-circuit — no embedding, no write, no audit.
	assert.Contains(t, create, "Duplicate",
		"create result must carry Duplicate flag for replay callers")
}

// UAT-9: Direct fragment read, list, and delete routes are removed from the client surface.
// AC trace: AC-27, AC-28, AC-29, AC-30, AC-31, AC-42, AC-48, AC-50.
func TestUAT9_DirectFragmentRoutesRemoved(t *testing.T) {
	router := readFile(t, "internal/http/router_protected.go")

	assert.NotContains(t, router, "fragmentGroup",
		"direct /fragments routes must not be mounted")
	for _, path := range []string{
		"internal/http/handler/fragment_read_handler.go",
		"internal/http/handler/fragment_list_handler.go",
		"internal/http/handler/fragment_delete_handler.go",
	} {
		_, err := os.Stat(repoPath(t, path))
		require.ErrorIs(t, err, os.ErrNotExist, "%s must not exist as a direct public handler", path)
	}
}

// UAT-10: Tool catalog + OpenAPI generator publish the same source-of-truth
// (Unit 20–23).
// AC trace: AC-32, AC-33, AC-34, AC-35, AC-50.
func TestUAT10_ToolCatalogAndOpenAPI(t *testing.T) {
	generator := readFile(t, "internal/openapi/generator.go")
	assert.Contains(t, generator, `"openapi": "3.0.3"`,
		"OpenAPI generator must emit spec version 3.0.3 (AC-34)")
	assert.Contains(t, generator, "registry",
		"OpenAPI generator must read from the shared registry, not redefine schemas")

	// In-process contract check: BuildDefault advertises every canonical tool
	// even with zero service wiring, so discovery never silently drops entries.
	reg, err := registry.BuildDefault(registry.Dependencies{})
	require.NoError(t, err)
	list := reg.List()
	seen := map[string]bool{}
	for _, tl := range list {
		seen[tl.Name] = true
	}
	for _, name := range []string{
		"recall_memory", "remember", "get_memory_placement", "dispute_memory_placement",
		"confirm_memory", "reflect_memories", "import_memories",
	} {
		assert.True(t, seen[name], "registry must list %q after BuildDefault", name)
	}
	for _, name := range []string{
		"list_recent_memories", "keyword_search", "semantic_search", "graph_query",
		"post_claim", "get_claim", "list_claims", "verify_claim", "promote_claim",
		"get_fact", "list_facts", "retract_fragment", "detect_community",
		"get_community_summary", "list_communities",
		"keyword-search", "semantic-search", "graph-query",
	} {
		assert.False(t, seen[name], "registry must not list removed direct client tool %q", name)
	}
	assert.GreaterOrEqual(t, len(list), 10, "BuildDefault must register the evidence-first memory surface")

	// remember stays part of the stable catalog even when the memory service is
	// not wired in this test harness.
	remember, ok := reg.Get("remember")
	require.True(t, ok)
	assert.Contains(t, remember.RequiredScopes, "write",
		"remember must require the write scope")
}

// UAT-11: MCP Streamable HTTP derives profile scope from the API key and reuses
// the shared registry (Unit 24).
// AC trace: AC-33, AC-37, AC-50.
func TestUAT11_MCPHTTPDiscovery(t *testing.T) {
	router := readFile(t, "internal/http/router_protected.go")
	assert.Contains(t, router, `protectedGroup("/mcp")`,
		"MCP must be exposed at /mcp")
	assert.Contains(t, router, "AuthMiddleware",
		"MCP must use bearer auth")
	assert.Contains(t, router, "ProfileResolutionMiddleware",
		"MCP must derive profile scope through profile resolution")

	handler := readFile(t, "internal/http/handler/mcp_handler.go")
	assert.Contains(t, handler, "HandlePost",
		"MCP must support Streamable HTTP POST")
	assert.Contains(t, handler, "text/event-stream",
		"MCP must support SSE responses when requested")

	server := readFile(t, "internal/mcp/server.go")
	assert.Contains(t, server, "ProtocolVersion",
		"MCP server must advertise its protocol version")
	assert.Contains(t, server, `"jsonrpc"`,
		"MCP server must speak JSON-RPC 2.0")
	assert.Contains(t, server, "registry.Registry",
		"MCP server takes the shared registry, not its own tool list")
}

// UAT-12: Hybrid recall merges semantic + keyword via RRF with team_id
// post-filter and fragment-only results (Unit 22).
// AC trace: AC-38, AC-39, AC-40, AC-51.
func TestUAT12_HybridRecallRanking(t *testing.T) {
	body := readFile(t, "internal/service/recallservice/recall.go")
	for _, sym := range []string{
		"RRFConstant", "OverfetchMultiplier",
		"RecallRequest", "RecallHit",
		"team_id",
	} {
		assert.Contains(t, body, sym,
			"recall.go must declare %q", sym)
	}
	// RRF is documented as the merge strategy (no weighted sum, no Borda count).
	assert.Contains(t, body, "Reciprocal Rank Fusion",
		"recall must document RRF as its merge strategy (AC-51)")

	// Public type sanity — a RecallRequest round-trips its fields so callers
	// compile against the same contract the service enforces.
	req := recallservice.RecallRequest{Query: "what are the UI settings?", Limit: 5}
	assert.Equal(t, "what are the UI settings?", req.Query)
	assert.Equal(t, 5, req.Limit)
}

// UAT-13: Recall fails closed on embedding errors and isolates by caller profile
// (Unit 22).
// AC trace: AC-39, AC-40, AC-47, AC-52.
func TestUAT13_RecallCrossProfileIsolation(t *testing.T) {
	body := readFile(t, "internal/service/recallservice/recall.go")
	// Fail-closed on embedding failure — no silent fallback to keyword-only.
	assert.Contains(t, body, "ErrEmbeddingUnavailable",
		"recall must expose a sanitized embedding-unavailable error (AC-40)")
	assert.Contains(t, body, "fail-closed",
		"recall must document fail-closed behavior")
	// SourceFragment-only and defensive post-filter by caller profile (AC-39, AC-47).
	assert.Contains(t, body, "SourceFragment",
		"recall must keep only SourceFragment-typed hits")
	assert.Contains(t, body, "h.ProfileID != profileID",
		"recall must defensively drop any hit that does not match caller profile")
}

// readFile returns the string contents of a repo-relative path, failing the
// test if the file is missing. Centralized so the "where did my artefact go?"
// error message stays uniform across UATs.
func readFile(t *testing.T, rel string) string {
	t.Helper()
	path := repoPath(t, rel)
	body, err := os.ReadFile(path)
	require.NoError(t, err, "%s must exist", rel)
	return string(body)
}
