package openapi

import (
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

	toolCatalogEntry := schemas["ToolCatalogEntry"].(map[string]any)
	toolCatalogProps := toolCatalogEntry["properties"].(map[string]any)
	for _, field := range []string{"contract_version", "feature_gate", "visibility"} {
		if _, ok := toolCatalogProps[field]; !ok {
			t.Fatalf("ToolCatalogEntry schema missing %s", field)
		}
	}
}
