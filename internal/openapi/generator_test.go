package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func testRegistry(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.BuildDefault(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	return reg
}

// TestGenerator_AISafeExcludesRuntimeOnlyRoutes verifies the public AI-safe
// spec does not include the wider runtime-only surface.
func TestGenerator_AISafeExcludesRuntimeOnlyRoutes(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}
	if _, present := paths["/api/v1/teams/{teamId}/query/stream"]; present {
		t.Errorf("runtime-only query stream path must NOT appear in ai-safe spec")
	}
	if _, present := paths["/mcp"]; present {
		t.Errorf("runtime-only MCP path must NOT appear in ai-safe spec")
	}
	if _, present := paths["/api/v1/teams/{teamId}"]; present {
		t.Errorf("runtime-only team route must NOT appear in ai-safe spec")
	}
	if _, present := paths["/api/v1/fragments"]; present {
		t.Errorf("ai-safe spec must not include direct fragment routes")
	}
	for _, path := range []string{"/api/v1/tools/remember", "/api/v1/tools/get_memory_placement", "/api/v1/tools/resolve_memory_placement"} {
		if _, present := paths[path]; !present {
			t.Errorf("ai-safe spec must include %s", path)
		}
	}
	for _, path := range []string{"/api/v1/tools/dispute_memory_placement", "/api/v1/tools/import_memories", "/api/v1/tools/reflect_memories", "/api/v1/tools/confirm_memory"} {
		if _, present := paths[path]; present {
			t.Errorf("ai-safe spec must not include removed path %s", path)
		}
	}
}

func TestGenerator_FullIncludesRuntimeOnlyRoutes(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	paths := spec["paths"].(map[string]any)
	if _, present := paths["/mcp"]; !present {
		t.Errorf("full spec must include /mcp")
	}
	if _, present := paths["/api/v1/teams/{teamId}"]; !present {
		t.Errorf("full spec must include /api/v1/teams/{teamId}")
	}
	if _, present := paths["/api/v1/teams/{teamId}/query/stream"]; present {
		t.Errorf("full spec must not include removed query stream path")
	}
	if _, present := paths["/api/v1/fragments"]; present {
		t.Errorf("full spec must not include direct fragment routes")
	}
}

func TestGenerator_MemoryPlacementToolRoutesUseRegistrySchemas(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths := spec["paths"].(map[string]any)
	rememberPath := paths["/api/v1/tools/remember"].(map[string]any)
	rememberOp := rememberPath["post"].(map[string]any)

	reqBody, ok := rememberOp["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("POST /api/v1/tools/remember requestBody missing")
	}
	reqContent := reqBody["content"].(map[string]any)
	reqJSON := reqContent["application/json"].(map[string]any)
	reqSchema := reqJSON["schema"].(map[string]any)
	if got := reqSchema["$ref"]; got != "#/components/schemas/RememberInput" {
		t.Fatalf("POST /api/v1/tools/remember requestBody $ref = %v; want #/components/schemas/RememberInput", got)
	}

	responses := rememberOp["responses"].(map[string]any)
	resp200, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatalf("POST /api/v1/tools/remember 200 response missing; have: %v", keysOf(responses))
	}
	resp200Content := resp200["content"].(map[string]any)
	resp200JSON := resp200Content["application/json"].(map[string]any)
	resp200Schema := resp200JSON["schema"].(map[string]any)
	if got := resp200Schema["$ref"]; got != "#/components/schemas/RememberOutput" {
		t.Fatalf("POST /api/v1/tools/remember 200 response $ref = %v; want #/components/schemas/RememberOutput", got)
	}

	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if _, has := schemas["RememberInput"]; !has {
		t.Fatalf("RememberInput schema missing from components")
	}
	if _, has := schemas["RememberOutput"]; !has {
		t.Fatalf("RememberOutput schema missing from components")
	}
}

func TestGenerator_ValidOpenAPIVersion(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Errorf("openapi = %v; want 3.0.3", spec["openapi"])
	}
}

func TestGenerator_CustomBuildInfoAndCloneSlice(t *testing.T) {
	g := NewWithInfo(testRegistry(t), DefaultRoutes(), BuildInfo{
		Title:       "custom dense-mem",
		Version:     "v-test",
		Description: "custom description",
	})
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	info := spec["info"].(map[string]any)
	if info["title"] != "custom dense-mem" || info["version"] != "v-test" {
		t.Fatalf("info = %v, want custom build info", info)
	}

	originalNested := map[string]any{"x": "y"}
	original := []any{map[string]any{"nested": []any{originalNested}}, "scalar"}
	clone := cloneSlice(original)
	cloneMap := clone[0].(map[string]any)
	cloneNested := cloneMap["nested"].([]any)[0].(map[string]any)
	cloneNested["x"] = "changed"

	if originalNested["x"] != "y" {
		t.Fatalf("cloneSlice mutated original nested map: %v", originalNested)
	}
	if clone[1] != "scalar" {
		t.Fatalf("cloneSlice scalar = %v, want scalar", clone[1])
	}
}

func TestGenerator_SecuritySchemesPresent(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatalf("components missing")
	}
	ss, ok := components["securitySchemes"].(map[string]any)
	if !ok {
		t.Fatalf("securitySchemes missing")
	}
	bearer, has := ss["BearerAuth"].(map[string]any)
	if !has {
		t.Fatalf("BearerAuth scheme missing")
	}
	if bearer["type"] != "http" || bearer["scheme"] != "bearer" {
		t.Errorf("BearerAuth = %#v; want http bearer scheme", bearer)
	}
	if _, has := ss["ProfileHeader"]; has {
		t.Errorf("ProfileHeader scheme should not be present")
	}
}

func TestGenerator_SchemasDerivedFromRegistry(t *testing.T) {
	reg, err := registry.BuildDefault(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	g := New(reg, DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)

	// remember -> RememberInput/Output per schemaNameFor naming
	if _, has := schemas["RememberInput"]; !has {
		t.Errorf("registry-derived schema RememberInput missing; have keys: %v", keysOf(schemas))
	}
	if _, has := schemas["RememberOutput"]; !has {
		t.Errorf("registry-derived schema RememberOutput missing")
	}
	if _, has := schemas["ErrorResponse"]; !has {
		t.Errorf("ErrorResponse schema missing")
	}
}

func TestGenerator_UnknownVariantErrors(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	if _, err := g.Generate(SpecVariant("bogus")); err == nil {
		t.Errorf("expected error for unknown variant")
	}
}

func TestGenerator_JSONRoundTrips(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), "\"openapi\":\"3.0.3\"") {
		t.Errorf("serialized spec missing version: %s", string(b)[:120])
	}
}

func TestGenerator_PathParamsDeclared(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	paths := spec["paths"].(map[string]any)
	path := paths["/api/v1/tools/{name}"].(map[string]any)
	post := path["post"].(map[string]any)
	params, ok := post["parameters"].([]map[string]any)
	if !ok || len(params) == 0 {
		t.Fatalf("POST /api/v1/tools/{name} missing parameters block")
	}
	found := false
	for _, p := range params {
		if p["name"] == "name" {
			found = true
		}
	}
	if !found {
		t.Errorf("path param 'name' not declared")
	}
}

func TestGenerateOmitsDirectFragmentRetractionRoute(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}

	const retractPath = "/api/v1/fragments/{id}/retract"
	if _, present := paths[retractPath]; present {
		t.Fatalf("direct fragment retraction route %q must not appear in ai-safe spec", retractPath)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestGeneratorRespectsExplicitSchemaRefs verifies that when a RouteDescriptor
// carries explicit RequestSchema / ResponseSchema / SuccessStatus / Tags, those
// values are used in preference to any ToolName-derived schemas and the default
// 200 status. This is the red-test gate for Unit 2.
func TestGeneratorRespectsExplicitSchemaRefs(t *testing.T) {
	route := RouteDescriptor{
		Method:         "POST",
		Path:           "/api/v1/claims",
		OperationID:    "createClaim",
		RequestSchema:  "ClaimRequest",
		ResponseSchema: "ClaimResponse",
		SuccessStatus:  201,
		Tags:           []string{"knowledge"},
		AISafe:         true,
		Description:    "Create a new claim.",
	}

	g := New(testRegistry(t), []RouteDescriptor{route})
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}
	pathItem, ok := paths["/api/v1/claims"].(map[string]any)
	if !ok {
		t.Fatalf("/api/v1/claims missing from paths; have: %v", keysOf(paths))
	}
	op, ok := pathItem["post"].(map[string]any)
	if !ok {
		t.Fatalf("POST operation missing from /api/v1/claims")
	}

	// --- Tags must reflect the explicit Tags field, not path inference ---
	tags, ok := op["tags"].([]string)
	if !ok {
		t.Fatalf("tags missing or wrong type: %T", op["tags"])
	}
	foundKnowledge := false
	for _, tag := range tags {
		if tag == "knowledge" {
			foundKnowledge = true
		}
	}
	if !foundKnowledge {
		t.Errorf("tags = %v; want to contain \"knowledge\"", tags)
	}

	// --- Success response must use 201, not 200 ---
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatalf("responses missing or wrong type")
	}
	if _, has := responses["201"]; !has {
		t.Errorf("expected 201 response; got keys: %v", keysOf(responses))
	}
	if _, has := responses["200"]; has {
		t.Errorf("expected no 200 response when SuccessStatus=201; got keys: %v", keysOf(responses))
	}

	// --- Request body must reference the explicit ClaimRequest schema ---
	reqBody, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody missing or wrong type")
	}
	content, ok := reqBody["content"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody.content missing")
	}
	appJSON, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody.content[application/json] missing")
	}
	reqSchema, ok := appJSON["schema"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody schema missing")
	}
	if got := reqSchema["$ref"]; got != "#/components/schemas/ClaimRequest" {
		t.Errorf("requestBody $ref = %v; want #/components/schemas/ClaimRequest", got)
	}

	// --- 201 response must reference the explicit ClaimResponse schema ---
	resp201, ok := responses["201"].(map[string]any)
	if !ok {
		t.Fatalf("201 response wrong type: %T", responses["201"])
	}
	resp201Content, ok := resp201["content"].(map[string]any)
	if !ok {
		t.Fatalf("201 response content missing")
	}
	resp201AppJSON, ok := resp201Content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("201 response application/json missing")
	}
	resp201Schema, ok := resp201AppJSON["schema"].(map[string]any)
	if !ok {
		t.Fatalf("201 response schema missing")
	}
	if got := resp201Schema["$ref"]; got != "#/components/schemas/ClaimResponse" {
		t.Errorf("201 response $ref = %v; want #/components/schemas/ClaimResponse", got)
	}
}

func TestGenerateOmitsDirectClaimRoutes(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}

	for _, p := range []string{
		"/api/v1/claims",
		"/api/v1/claims/{id}",
		"/api/v1/claims/{id}/verify",
		"/api/v1/claims/{id}/promote",
	} {
		if _, present := paths[p]; present {
			t.Errorf("direct claim route %q must not appear in ai-safe spec", p)
		}
	}
}

// TestGenerator_CrossProfileIsolation verifies that spec generation is
// profile-agnostic: two invocations produce identical output (no per-profile
// state leaks into the static spec) and no hardcoded profile identifiers
// appear in the generated document.
func TestGenerator_CrossProfileIsolation(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())

	spec1, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	spec2, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	b1, err := json.Marshal(spec1)
	if err != nil {
		t.Fatalf("marshal spec1: %v", err)
	}
	b2, err := json.Marshal(spec2)
	if err != nil {
		t.Fatalf("marshal spec2: %v", err)
	}

	// Idempotent output proves no mutable per-call state bleeds through.
	if string(b1) != string(b2) {
		t.Error("spec generation is not idempotent — per-profile state may be leaking")
	}

	// No hardcoded profile IDs should appear in the spec.
	specStr := string(b1)
	for _, needle := range []string{"profile_A", "profile_B", "profileA", "profileB"} {
		if strings.Contains(specStr, needle) {
			t.Errorf("spec contains hardcoded profile identifier %q", needle)
		}
	}
}

func TestGenerateOmitsDirectVerifyRoute(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}

	const verifyPath = "/api/v1/claims/{id}/verify"
	if _, present := paths[verifyPath]; present {
		t.Fatalf("direct verify route %q must not appear in ai-safe spec", verifyPath)
	}
}

func TestGenerateOmitsDirectPromoteRoute(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())
	spec, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type")
	}

	const promotePath = "/api/v1/claims/{id}/promote"
	if _, present := paths[promotePath]; present {
		t.Fatalf("direct promote route %q must not appear in ai-safe spec", promotePath)
	}
}

// TestGenerateOmitsRemovedControlPlaneRoutes verifies the generated specs no
// longer surface the removed raw-Cypher HTTP path.
func TestGenerateOmitsRemovedControlPlaneRoutes(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())

	aiSafe, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate(AISafe): %v", err)
	}
	aiSafePaths, ok := aiSafe["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type in ai-safe spec")
	}
	legacyControlPlaneSegment := "ad" + "min"
	removedRawCypherPath := "/api/v1/" + legacyControlPlaneSegment + "/graph/query"
	if _, present := aiSafePaths[removedRawCypherPath]; present {
		t.Errorf("removed raw-Cypher path must NOT appear in ai-safe spec")
	}

	full, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate(Full): %v", err)
	}
	fullPaths, ok := full["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type in full spec")
	}
	if _, present := fullPaths[removedRawCypherPath]; present {
		t.Errorf("removed raw-Cypher path must NOT appear in full spec")
	}
}

func TestGenerateIncludesGenericToolExecuteRoute(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())

	aiSafe, err := g.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate(AISafe): %v", err)
	}
	aiSafePaths := aiSafe["paths"].(map[string]any)
	if _, present := aiSafePaths["/api/v1/tools/{name}"]; present {
		t.Fatalf("generic tool execute route must not appear in ai-safe spec")
	}

	full, err := g.Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate(Full): %v", err)
	}
	fullPaths := full["paths"].(map[string]any)
	pathItem, present := fullPaths["/api/v1/tools/{name}"]
	if !present {
		t.Fatalf("generic tool execute route missing from full spec; have: %v", keysOf(fullPaths))
	}

	postOp := pathItem.(map[string]any)["post"].(map[string]any)
	if postOp["operationId"] != "executeTool" {
		t.Errorf("operationId = %v; want executeTool", postOp["operationId"])
	}

	reqBody := postOp["requestBody"].(map[string]any)
	reqSchema := reqBody["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if got := reqSchema["$ref"]; got != "#/components/schemas/ToolExecuteRequest" {
		t.Errorf("requestBody $ref = %v; want #/components/schemas/ToolExecuteRequest", got)
	}

	respSchema := postOp["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if got := respSchema["$ref"]; got != "#/components/schemas/ToolExecuteResponse" {
		t.Errorf("200 response $ref = %v; want #/components/schemas/ToolExecuteResponse", got)
	}
}
