package openapi

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestV2ContractOpenAPIDerivesSchemasFromRegistry(t *testing.T) {
	reg := registry.New()
	for _, tool := range registry.V2ContractTools() {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	routes := []RouteDescriptor{
		{
			Method:      "POST",
			Path:        "/api/v2/tools/remember",
			OperationID: "v2Remember",
			ToolName:    registry.V2ToolRemember,
			AISafe:      true,
		},
		{
			Method:      "POST",
			Path:        "/api/v2/tools/correct_entity_resolution",
			OperationID: "v2CorrectEntityResolution",
			ToolName:    registry.V2ToolCorrectEntityResolution,
			AISafe:      true,
		},
	}

	gen := NewWithInfo(reg, routes, BuildInfo{
		Title:       "dense-mem v2 contract",
		Version:     domain.V2ContractVersion,
		Description: "Dormant V2 contract spec.",
	})
	doc, err := gen.Generate(SpecVariantAISafe)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	components := doc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	remember, _ := reg.Get(registry.V2ToolRemember)
	rememberInput := schemaNameFor(registry.V2ToolRemember, "Input")
	if !reflect.DeepEqual(schemas[rememberInput], remember.InputSchema) {
		t.Fatalf("remember schema not derived from registry")
	}
	rememberProps := remember.InputSchema["properties"].(map[string]any)
	if _, ok := rememberProps["contract_version"]; ok {
		t.Fatal("remember OpenAPI schema exposes payload contract_version")
	}
	if remember.InputSchema["x-contract-version"] != domain.V2ContractVersion {
		t.Fatalf("remember OpenAPI x-contract-version = %v", remember.InputSchema["x-contract-version"])
	}
	correct, _ := reg.Get(registry.V2ToolCorrectEntityResolution)
	correctInput := schemaNameFor(registry.V2ToolCorrectEntityResolution, "Input")
	if !reflect.DeepEqual(schemas[correctInput], correct.InputSchema) {
		t.Fatalf("correct_entity_resolution schema not derived from registry")
	}

	paths := doc["paths"].(map[string]any)
	rememberPost := paths["/api/v2/tools/remember"].(map[string]any)["post"].(map[string]any)
	reqBody := rememberPost["requestBody"].(map[string]any)
	content := reqBody["content"].(map[string]any)
	jsonMedia := content["application/json"].(map[string]any)
	reqSchema := jsonMedia["schema"].(map[string]any)
	if got := reqSchema["$ref"]; got != "#/components/schemas/"+rememberInput {
		t.Fatalf("remember request ref = %v", got)
	}
	if !reflect.DeepEqual(schemas["DenseMemV2Error"], registry.V2PublicErrorSchema()) {
		t.Fatal("V2 public error schema not derived from registry contract")
	}
	responses := rememberPost["responses"].(map[string]any)
	badRequest := responses["400"].(map[string]any)
	errorContent := badRequest["content"].(map[string]any)
	errorMedia := errorContent["application/json"].(map[string]any)
	errorSchema := errorMedia["schema"].(map[string]any)
	if got := errorSchema["$ref"]; got != "#/components/schemas/DenseMemV2Error" {
		t.Fatalf("remember 400 schema ref = %v", got)
	}

	toolCatalogEntry := schemas["ToolCatalogEntry"].(map[string]any)
	toolCatalogProps := toolCatalogEntry["properties"].(map[string]any)
	for _, field := range []string{"contract_version", "feature_gate", "visibility"} {
		if _, ok := toolCatalogProps[field]; !ok {
			t.Fatalf("ToolCatalogEntry schema missing %s", field)
		}
	}
}

func TestV2HTTPRegistryOpenAPIOmitsMCPOnlyMemoryToolRoutes(t *testing.T) {
	uat, err := registry.BuildActive(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	httpReg, err := registry.HTTPRegistryView(uat)
	if err != nil {
		t.Fatalf("HTTPRegistryView: %v", err)
	}
	doc, err := New(httpReg, DefaultRoutes()).Generate(SpecVariantFull)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths := doc["paths"].(map[string]any)
	for _, path := range []string{
		"/api/v1/tools/remember",
		"/api/v1/tools/get_memory_placement",
		"/api/v1/tools/recall_memory",
	} {
		if _, ok := paths[path]; ok {
			t.Fatalf("OpenAPI exposed MCP-only V2 memory tool route %s", path)
		}
	}
	if _, ok := paths["/api/v1/tools/{name}"]; !ok {
		t.Fatal("OpenAPI omitted the generic tool execution route")
	}

	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"RememberInput", "GetMemoryPlacementInput", "RecallMemoryInput"} {
		if _, ok := schemas[name]; ok {
			t.Fatalf("OpenAPI exposed MCP-only V2 memory tool schema %s", name)
		}
	}
}

func TestV2ContractOpenAPIRoundTripsGoldenFixtures(t *testing.T) {
	reg := registry.New()
	for _, tool := range registry.V2ContractTools() {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := NewWithInfo(reg, nil, BuildInfo{Version: domain.V2ContractVersion}).Generate(SpecVariantFull)
	if err != nil {
		t.Fatal(err)
	}
	components := doc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	for _, fixture := range readOpenAPIV2ContractFixtures(t) {
		if !fixture.Valid {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			input := schemas[schemaNameFor(fixture.Tool, "Input")].(map[string]any)
			if err := registry.ValidateInput(registry.Tool{InputSchema: input}, fixture.Input); err != nil {
				t.Fatalf("OpenAPI input schema: %v", err)
			}
			output := schemas[schemaNameFor(fixture.Tool, "Output")].(map[string]any)
			if err := registry.ValidateInput(registry.Tool{InputSchema: output}, fixture.Output); err != nil {
				t.Fatalf("OpenAPI output schema: %v", err)
			}
		})
	}
}

type openAPIV2ContractFixture struct {
	Name   string         `json:"name"`
	Tool   string         `json:"tool"`
	Valid  bool           `json:"valid"`
	Input  map[string]any `json:"input"`
	Output map[string]any `json:"output"`
}

func readOpenAPIV2ContractFixtures(t *testing.T) []openAPIV2ContractFixture {
	t.Helper()
	data, err := os.ReadFile("../tools/registry/testdata/v2_contract_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []openAPIV2ContractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}
